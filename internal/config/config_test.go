package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRealConfigLoadsWithRoutingBudgetsAndBlockedPaths(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "testdata", "sentinela.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Repos) != 11 {
		t.Errorf("repos = %d, quer 11", len(cfg.Repos))
	}
	if cfg.Budget.PerRunUSD != 5 || cfg.Budget.PerPRUSD != 3 {
		t.Errorf("budget = %+v", cfg.Budget)
	}
	r, ok := cfg.Repo("acme/monorepo-poliglota")
	if !ok || len(r.Modules) != 3 || r.FallbackTeam != "plataforma" {
		t.Errorf("monorepo-poliglota = %+v", r)
	}
	if r, _ := cfg.Repo("acme/app-java-angular"); r.OwnersSource != OwnersBoth || r.DefaultBranch != "main" {
		t.Errorf("padrões não aplicados ao app-java-angular: %+v", r)
	}
	if cfg.Models.Executor != "claude-opus-5-5" || cfg.Models.Orchestrator != "claude-sonnet-5-5" || cfg.Models.Triage != "claude-haiku-5-5" {
		t.Errorf("models = %+v", cfg.Models)
	}
	if cfg.LLM.Provider != ProviderClaudeCode || cfg.LLM.ClaudeCode.Concurrency != 2 || cfg.LLM.ClaudeCode.MaxCallsPerRun != 200 {
		t.Errorf("llm = %+v", cfg.LLM)
	}
	if cfg.Models.Refusal.Action != RefusalFallbackModel || cfg.Models.Refusal.FallbackModel == "" {
		t.Errorf("refusal = %+v", cfg.Models.Refusal)
	}
	if !cfg.IsBlockedPath("go/internal/financeiro/boleto.go") || cfg.IsBlockedPath("go/internal/turmas/x.go") {
		t.Error("blocked_paths não casou como esperado")
	}
}

func TestInvalidConfigsAreRejectedWithClearErrors(t *testing.T) {
	base := `
defaults: { linear: { team: PLAT } }
budget: { per_run_usd: 1, per_pr_usd: 1 }
models: { triage: m, analysis: m, architect: m, judge: m, curator: m, spec_writer: m, executor: m, reviewer_security: m, reviewer_code: m, reviewer_reliability: m, reviewer_domain: m, approver: m }
pricing: { models: { m: { input: 1, output: 1 } } }
teams: { a: { assignee: none } }
`
	cases := []struct {
		name, yaml, want string
	}{
		{"campo desconhecido", base + "foo: 1\n", "field foo not found"},
		{"módulo aponta para time inexistente", base + "repos: [{ name: o/r, modules: [{ path: 'x/**', team: zz }] }]\n", `time "zz" não existe`},
		{"assignee lead sem lead", strings.Replace(base, "assignee: none", "assignee: lead", 1), "assignee lead exige lead"},
		{"round_robin sem members", strings.Replace(base, "assignee: none", "assignee: round_robin", 1), "round_robin exige members"},
		{"modelo sem preço", strings.Replace(base, "triage: m,", "triage: outro,", 1), `modelo "outro" sem preço`},
		{"papel da linha de execução ausente", strings.Replace(base, " executor: m,", "", 1), "models.executor é obrigatório"},
		{"ação de recusa inválida", strings.Replace(base, "approver: m }", "approver: m, refusal: { action: retry } }", 1), "models.refusal.action inválido"},
		{"fallback de recusa sem modelo", strings.Replace(base, "approver: m }", "approver: m, refusal: { action: fallback_model } }", 1), "fallback_model é obrigatório"},
		{"fallback de recusa sem preço", strings.Replace(base, "approver: m }", "approver: m, refusal: { action: fallback_model, fallback_model: x } }", 1), `modelo "x" sem preço`},
		{"orçamento zerado", strings.Replace(base, "per_run_usd: 1", "per_run_usd: 0", 1), "per_run_usd deve ser maior que zero"},
		{"owners_source inválido", base + "repos: [{ name: o/r, owners_source: github }]\n", "owners_source inválido"},
		{"repo sem owner", base + "repos: [{ name: r }]\n", "deve ser owner/repo"},
		{"provider desconhecido", base + "llm: { provider: openai }\n", "llm.provider inválido"},
		{"replay sem diretório", base + "llm: { provider: replay }\n", "llm.replay.dir é obrigatório"},
		{"repo duplicado", base + "repos: [{ name: o/r }, { name: o/r }]\n", "duplicado"},
		{"módulo sem time e sem ignore", base + "repos: [{ name: o/r, modules: [{ path: 'x/**' }] }]\n", "team é obrigatório"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("erro = %v, quer conter %q", err, tc.want)
			}
		})
	}
}

func TestLabelsOfTheOtherSystemAreRejected(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "testdata", "sentinela.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Defaults.Linear.Labels = append(cfg.Defaults.Linear.Labels, "claude-analysis")
	cfg.Labels.Trigger = "Claude-fix"
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), `"claude-analysis"`) || !strings.Contains(err.Error(), `"Claude-fix"`) {
		t.Errorf("validate = %v", err)
	}
	if got := cfg.IssueLabels(cfg.Teams["time-exemplo"]); len(got) != 2 || got[0] != "sentinela" {
		t.Errorf("IssueLabels = %v", got)
	}
}

// O exemplo distribuído com o projeto precisa carregar e passar na validação local.
func TestExampleConfigIsValid(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "sentinela.example.yaml"))
	if err != nil {
		t.Fatalf("sentinela.example.yaml inválido: %v", err)
	}
	if len(cfg.Repos) != 2 || cfg.Publish.Enabled || cfg.GitHubApps.ReviewerApproves {
		t.Errorf("exemplo deveria ter 2 repos, publicação desligada e revisora sem aprovar: %+v %+v", cfg.Publish, cfg.GitHubApps)
	}
}

// exampleWith é o sentinela.example.yaml com overrides de modelo acrescentados a um time e a um repo.
func exampleWith(t *testing.T, teamModels, repoModels string) (*Config, error) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "sentinela.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	s = strings.Replace(s, "    policy: { auto_approve: false, blocked: true }       # domínio sensível: nunca auto-aprova\n",
		"    policy: { auto_approve: false, blocked: true }\n"+teamModels, 1)
	s = strings.Replace(s, "  - name: acme/monorepo\n", "  - name: acme/monorepo\n"+repoModels, 1)
	return Parse([]byte(s))
}

func TestModelsFollowTeamThenRepoThenGlobalWithoutChangingTheDefaults(t *testing.T) {
	cfg, err := exampleWith(t,
		"    models: { analysis: claude-opus-5-5 }\n",
		"    models: { triage: claude-sonnet-5-5, analysis: claude-sonnet-5-5, reviewer_code: claude-opus-5-5 }\n")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, repo, team                        string
		triage, analysis, judge, code, shepherd string
	}{
		{"sem override: os padrões do models global", "acme/outro-repo", "", "claude-opus-5-5", "claude-opus-5-5", "claude-sonnet-5-5", "claude-sonnet-5-5", "claude-sonnet-5-5"},
		{"repo troca o Opus da triagem por Sonnet", "acme/monorepo", "platform", "claude-sonnet-5-5", "claude-sonnet-5-5", "claude-sonnet-5-5", "claude-opus-5-5", "claude-opus-5-5"},
		{"o time dono vence o repo na análise", "acme/monorepo", "payments", "claude-sonnet-5-5", "claude-opus-5-5", "claude-sonnet-5-5", "claude-opus-5-5", "claude-opus-5-5"},
		{"o time vale também fora do monorepo", "acme/app-java-angular", "payments", "claude-opus-5-5", "claude-opus-5-5", "claude-sonnet-5-5", "claude-sonnet-5-5", "claude-sonnet-5-5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := cfg.ModelsFor(tc.repo, tc.team)
			got := []string{m.Triage, m.Analysis, m.Judge, m.ReviewerCode, m.Shepherd}
			want := []string{tc.triage, tc.analysis, tc.judge, tc.code, tc.shepherd}
			if strings.Join(got, " ") != strings.Join(want, " ") {
				t.Errorf("triage analysis judge reviewer_code shepherd = %v, quer %v", got, want)
			}
		})
	}
	if cfg.Models.Triage != "claude-opus-5-5" || cfg.Models.Executor != "claude-sonnet-5-5" {
		t.Errorf("o models global mudou: %+v", cfg.Models.ModelRoles)
	}

	explained := map[string]ModelChoice{}
	for _, c := range cfg.ExplainModels("acme/monorepo", "payments") {
		explained[c.Role] = c
	}
	for role, from := range map[string]string{"triage": "repos[acme/monorepo].models", "analysis": "teams.payments.models", "judge": "models", "shepherd": "repos[acme/monorepo].models"} {
		if explained[role].From != from {
			t.Errorf("%s definido em %q, quer %q", role, explained[role].From, from)
		}
	}
}

func TestModelOverrideNeedsAPriceAndOnlyAcceptsRoles(t *testing.T) {
	if _, err := exampleWith(t, "    models: { triage: claude-sem-preco }\n", ""); err == nil || !strings.Contains(err.Error(), `modelo "claude-sem-preco" sem preço`) {
		t.Errorf("override sem preço deveria falhar: %v", err)
	}
	if _, err := exampleWith(t, "", "    models: { use_batch: false }\n"); err == nil {
		t.Error("use_batch só existe no models global; no repo deveria falhar")
	}
}

func TestOrchestratorNotesAndRefusalFallbackFollowTeamThenRepoThenGlobal(t *testing.T) {
	cfg, err := exampleWith(t,
		"    models: { orchestrator: claude-opus-5-5, notes: claude-opus-5-5, refusal_fallback: claude-opus-5-5 }\n",
		"    models: { notes: claude-haiku-5-5 }\n")
	if err != nil {
		t.Fatal(err)
	}
	global := cfg.ModelsFor("", "")
	if global.Orchestrator != "claude-sonnet-5-5" || global.Notes != "claude-sonnet-5-5" || global.RefusalFallback != "claude-sonnet-5-5" {
		t.Errorf("padrões = %+v", global)
	}
	if m := cfg.ModelsFor("acme/monorepo", "platform"); m.Notes != "claude-haiku-5-5" || m.Orchestrator != "claude-sonnet-5-5" || m.RefusalFallback != "claude-sonnet-5-5" {
		t.Errorf("repo = %+v", m)
	}
	if m := cfg.ModelsFor("acme/monorepo", "payments"); m.Notes != "claude-opus-5-5" || m.Orchestrator != "claude-opus-5-5" || m.RefusalFallback != "claude-opus-5-5" {
		t.Errorf("time = %+v", m)
	}
	from := map[string]string{}
	for _, c := range cfg.ExplainModels("acme/monorepo", "payments") {
		from[c.Role] = c.From
	}
	if from["refusal_fallback"] != "teams.payments.models" || from["orchestrator"] != "teams.payments.models" {
		t.Errorf("explain = %v", from)
	}
}

// Configs anteriores às chaves orchestrator e notes continuam carregando, com os modelos que
// esses papéis usavam (spec_writer e analysis); refusal_fallback no global é recusado.
func TestOldConfigsKeepTheirModelsAndGlobalRefusalFallbackIsRejected(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "sentinela.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	old := strings.NewReplacer("  notes: claude-sonnet-5-5         # intérprete de notas de review\n", "",
		"  orchestrator: claude-sonnet-5-5  # sessão da skill sentinela-fix, que coordena os subagentes\n", "",
		"  spec_writer: claude-sonnet-5-5\n", "  spec_writer: claude-opus-5-5\n").Replace(string(data))
	cfg, err := Parse([]byte(old))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Models.Orchestrator != "claude-opus-5-5" || cfg.Models.Notes != cfg.Models.Analysis {
		t.Errorf("orchestrator = %s, notes = %s", cfg.Models.Orchestrator, cfg.Models.Notes)
	}
	bad := strings.Replace(string(data), "  use_batch: true", "  refusal_fallback: claude-haiku-5-5\n  use_batch: true", 1)
	if _, err := Parse([]byte(bad)); err == nil || !strings.Contains(err.Error(), "models.refusal_fallback só vale") {
		t.Errorf("refusal_fallback no global deveria falhar: %v", err)
	}
}
