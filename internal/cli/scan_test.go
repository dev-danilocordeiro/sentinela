package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/discovery"
	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
)

// scan --all: já analisados sempre; novos até max_new_repos_per_run, na ordem do yaml.
func TestSelectAllReposLimitsNeverAnalyzedRepos(t *testing.T) {
	cfg, err := config.Load(rootConfig)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range cfg.Repos {
		names = append(names, r.Name)
	}
	if len(names) < 3 {
		t.Fatalf("fixture com %d repos, quer 3+", len(names))
	}
	analyzed := []string{names[1], "outro/repo-fora-do-yaml"}
	for _, tc := range []struct {
		max        int
		run, later []string
	}{
		{1, []string{names[0], names[1]}, names[2:]},
		{0, []string{names[1]}, append([]string{names[0]}, names[2:]...)},
		{len(names), names, nil},
	} {
		n := tc.max
		cfg.Discovery.MaxNewReposPerRun = &n
		run, later := selectAllRepos(cfg, analyzed)
		if strings.Join(run, ",") != strings.Join(tc.run, ",") || strings.Join(later, ",") != strings.Join(tc.later, ",") {
			t.Errorf("max %d: run %v later %v; quer %v e %v", tc.max, run, later, tc.run, tc.later)
		}
	}
	if *mustParseDefault(t).Discovery.MaxNewReposPerRun != 1 {
		t.Error("padrão de max_new_repos_per_run deveria ser 1")
	}
}

func mustParseDefault(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(rootConfig)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// A falha da publicação aparece no terminal com os pendentes do repo e vira erro (código de
// saída diferente de 0), também na rodada sem mudanças; sem falha, nada muda.
func TestPublishOutcomeShowsTheErrorAndThePending(t *testing.T) {
	paused := time.Date(2026, 10, 17, 12, 20, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		res   discovery.Result
		want  []string
		isErr bool
	}{
		{"falha numa issue", discovery.Result{PublishErr: fmt.Errorf("%w: 1 issue(s) falharam e ficam pendentes", linear.ErrPublishFailed)},
			[]string{"erro: publicação no Linear incompleta", "3 achado(s) de acme/svc continuam sem issue", "sentinela scan --repo acme/svc"}, true},
		{"limite de uso", discovery.Result{PublishErr: linear.ErrUsagePaused, Stats: discovery.Stats{Issues: &linear.PublishResult{PausedUntil: &paused}}},
			[]string{"erro: publicação pausada pelo limite de uso", "3 achado(s) de acme/svc continuam sem issue", "o serve retoma depois de 17/10 12:20"}, true},
		{"sem falha", discovery.Result{}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			err := publishOutcome(&b, "acme/svc", tc.res, 3)
			if (err != nil) != tc.isErr {
				t.Fatalf("err = %v", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(b.String(), w) {
					t.Errorf("saída sem %q:\n%s", w, b.String())
				}
			}
			if !tc.isErr && b.Len() != 0 {
				t.Errorf("sem falha, sem saída: %q", b.String())
			}
		})
	}
}

// A estimativa avisa quando o pior caso passa do teto da rodada e diz o que fazer; dentro do
// teto, não avisa.
func TestEstimateWarnsWhenTheWorstCaseExceedsTheCap(t *testing.T) {
	calls := discovery.Estimate{ExpectedCalls: 72, WorstCalls: 637, Agents: []discovery.AgentEstimate{
		{Agent: "triager", Calls: 28, WorstCalls: 28}, {Agent: "judge", Calls: 29, WorstCalls: 580},
	}}
	usd := discovery.Estimate{Expected: llm.USD(2), Worst: llm.USD(14), Agents: []discovery.AgentEstimate{
		{Agent: "analyst", Worst: llm.USD(4)}, {Agent: "judge", Worst: llm.USD(10)},
	}}
	for _, tc := range []struct {
		name     string
		e        discovery.Estimate
		provider string
		budget   llm.NanoUSD
		maxCalls int
		want     []string
	}{
		{"chamadas acima do teto", calls, config.ProviderClaudeCode, 0, 200, []string{
			"aviso: o pior caso (637 chamadas) passa do teto de 200 (llm.claude_code.max_calls_per_run)",
			"judge", "discovery.max_findings_per_package", "aborted_budget"}},
		{"chamadas dentro do teto", calls, config.ProviderClaudeCode, 0, 700, nil},
		{"dólar acima do teto", usd, config.ProviderAnthropicAPI, llm.USD(10), 0, []string{
			"aviso: o pior caso ($14.000000) passa do teto de $10.000000 (budget.per_run_usd ou --budget-usd)", "judge"}},
		{"dólar dentro do teto", usd, config.ProviderAnthropicAPI, llm.USD(20), 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			printEstimate(&b, tc.e, tc.provider, tc.budget, tc.maxCalls)
			for _, w := range tc.want {
				if !strings.Contains(b.String(), w) {
					t.Errorf("saída sem %q:\n%s", w, b.String())
				}
			}
			if tc.want == nil && strings.Contains(b.String(), "aviso:") {
				t.Errorf("sem aviso dentro do teto:\n%s", b.String())
			}
		})
	}
}

// As unidades de build aparecem agrupadas por tipo no resumo do scan (Fase C, passo 3).
func TestUnitsSummaryGroupsByKind(t *testing.T) {
	units := []repo.BuildUnit{{Dir: "api", Kind: repo.KindMaven}, {Dir: "api/domain", Kind: repo.KindMaven}, {Dir: "web", Kind: repo.KindNPM}, {Dir: ".", Kind: repo.KindGradle}}
	if got := unitsSummary(units); got != "gradle: . · maven: api, api/domain · npm: web" {
		t.Errorf("resumo = %q", got)
	}
	if got := unitsSummary(nil); got != "nenhuma (sem manifesto de build)" {
		t.Errorf("vazio = %q", got)
	}
}
