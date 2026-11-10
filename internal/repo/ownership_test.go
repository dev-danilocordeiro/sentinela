package repo

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
)

const (
	monorepo = "acme/monorepo-poliglota"
	appJava  = "acme/app-java-angular"
	ledger   = "acme/razao-core"
)

// Fixtures reais: o sentinela.yaml da raiz e o CODEOWNERS de teste do monorepo-poliglota.
func loadFixtures(t *testing.T) (*config.Config, *Codeowners) {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "..", "testdata", "sentinela.yaml"))
	if err != nil {
		t.Fatalf("carregando sentinela.yaml: %v", err)
	}
	f, err := os.Open(filepath.Join("..", "..", "testdata", "CODEOWNERS"))
	if err != nil {
		t.Fatalf("abrindo CODEOWNERS: %v", err)
	}
	defer func() { _ = f.Close() }()
	co, err := ParseCodeowners(f)
	if err != nil {
		t.Fatalf("interpretando CODEOWNERS: %v", err)
	}
	return cfg, co
}

type want struct {
	team, rule, linearTeam, project, assignee, triage string
	subscribers                                       []string
	ignored                                           bool
	warning                                           string // substring esperada em algum aviso
	blocked                                           bool
}

func check(t *testing.T, got Resolution, w want) {
	t.Helper()
	if got.Ignored != w.ignored {
		t.Fatalf("ignored = %v, quer %v (trace: %v)", got.Ignored, w.ignored, got.Trace)
	}
	if got.Team != w.team || got.Rule != w.rule {
		t.Fatalf("team/rule = %q/%q, quer %q/%q\ntrace: %s", got.Team, got.Rule, w.team, w.rule, strings.Join(got.Trace, "\n"))
	}
	if w.linearTeam != "" && got.LinearTeam != w.linearTeam {
		t.Errorf("linear_team = %q, quer %q", got.LinearTeam, w.linearTeam)
	}
	if w.project != "" && got.LinearProject != w.project {
		t.Errorf("linear_project = %q, quer %q", got.LinearProject, w.project)
	}
	if got.Assignee != w.assignee {
		t.Errorf("assignee = %q, quer %q", got.Assignee, w.assignee)
	}
	if w.triage != "" && got.TriageState != w.triage {
		t.Errorf("triage_state = %q, quer %q", got.TriageState, w.triage)
	}
	if !slices.Equal(got.Subscribers, w.subscribers) && (len(got.Subscribers) > 0 || len(w.subscribers) > 0) {
		t.Errorf("subscribers = %v, quer %v", got.Subscribers, w.subscribers)
	}
	if w.warning == "" && len(got.Warnings) > 0 {
		t.Errorf("avisos inesperados: %v", got.Warnings)
	}
	if w.warning != "" && !slices.ContainsFunc(got.Warnings, func(s string) bool { return strings.Contains(s, w.warning) }) {
		t.Errorf("avisos = %v, quer um contendo %q", got.Warnings, w.warning)
	}
	blocked := got.Policy.Blocked != nil && *got.Policy.Blocked
	if blocked != w.blocked {
		t.Errorf("policy.blocked = %v, quer %v", blocked, w.blocked)
	}
}

func TestResolverWithRealFixtures(t *testing.T) {
	cfg, co := loadFixtures(t)
	r := NewResolver(cfg, StaticCodeowners{monorepo: co})

	cases := []struct {
		name, repo, path string
		want             want
	}{
		{
			name: "repo de um time resolve pelo repos[].team e usa o projeto do repo",
			repo: appJava, path: "internal/shortener/handler.go",
			want: want{team: "time-exemplo", rule: "repo", linearTeam: "EX", project: "app-java-angular", triage: "Backlog"},
		},
		{
			name: "repo de um time do domínio financeiro herda a política bloqueada e o lead",
			repo: ledger, path: "src/main/kotlin/Ledger.kt",
			want: want{team: "financeiro", rule: "repo", project: "Sentinela · Financeiro", assignee: "lead@example.com", blocked: true},
		},
		{
			name: "monorepo com config: modules[].path vence o CODEOWNERS",
			repo: monorepo, path: "java/scripts/deploy.sh",
			want: want{team: "plataforma", rule: "config:java/scripts/**", project: "Sentinela · Plataforma"},
		},
		{
			name: "monorepo só com CODEOWNERS: diretório do acadêmico em Go",
			repo: monorepo, path: "go/internal/matricula/service.go",
			want: want{team: "academico", rule: "codeowners:L10", project: "Sentinela · Acadêmico"},
		},
		{
			name: "monorepo só com CODEOWNERS: arquivo aninhado em Kotlin do financeiro",
			repo: monorepo, path: "kotlin/src/main/kotlin/escola/mensalidades/calc/Juros.kt",
			want: want{team: "financeiro", rule: "codeowners:L22", assignee: "lead@example.com", blocked: true},
		},
		{
			name: "monorepo só com CODEOWNERS: caminho genérico cai na regra curinga",
			repo: monorepo, path: "README.md",
			want: want{team: "plataforma", rule: "codeowners:L7"},
		},
		{
			name: "regra com vários donos: o primeiro é o principal e o segundo vira inscrito",
			repo: monorepo, path: "java/src/main/resources/db/migration/V1__init.sql",
			want: want{team: "plataforma", rule: "codeowners:L26", subscribers: []string{"academico"}},
		},
		{
			name: "owner sem time mapeado gera aviso e cai no fallback_team, sem falha silenciosa",
			repo: monorepo, path: "python/tests/test_matricula.py",
			want: want{team: "plataforma", rule: "fallback", warning: "@acme/qa"},
		},
		{
			name: "usuário individual resolve para o time de que é membro",
			repo: monorepo, path: "go/archtest/arch_test.go",
			want: want{team: "plataforma", rule: "codeowners:L30"},
		},
		{
			name: "EX-13: Dockerfile dentro de módulo do acadêmico vai para a plataforma pelo override",
			repo: monorepo, path: "java/servico-turmas/Dockerfile",
			want: want{team: "plataforma", rule: "config:**/Dockerfile", project: "Sentinela · Plataforma"},
		},
		{
			name: "caminho com barra inicial é normalizado",
			repo: monorepo, path: "/go/internal/turmas/x.go",
			want: want{team: "academico", rule: "codeowners:L11"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Resolve(tc.repo, tc.path)
			if err != nil {
				t.Fatal(err)
			}
			check(t, got, tc.want)
		})
	}
}

const syntheticYAML = `
defaults:
  linear: { team: PLAT, triage_state: Triage, project: "Sentinela" }
  owners_source: both
  policy: { auto_approve: false, max_lines: 50 }
budget: { per_run_usd: 1, per_pr_usd: 1 }
models: { triage: m, analysis: m, architect: m, judge: m, curator: m, spec_writer: m, executor: m, reviewer_security: m, reviewer_code: m, reviewer_reliability: m, reviewer_domain: m, approver: m }
pricing: { models: { m: { input: 1, output: 1 } } }
teams:
  payments:
    github: ["@acme/payments"]
    linear: { team: PAY, project: "Sentinela · Payments", triage_state: Inbox }
    assignee: lead
    lead: maria@empresa.com
    notify: { slack: "#payments-alerts" }
    policy: { auto_approve: false, blocked: true }
  platform:
    github: ["@acme/platform", "@acme/sre"]
    linear: { team: PLAT, project: "Sentinela · Platform" }
    assignee: round_robin
    members: [joao, ana]
    policy: { auto_approve: true }
repos:
  - name: acme/monorepo
    modules:
      - { path: "services/**", team: platform }
      - { path: "services/billing/**", team: payments }
      - { path: "services/legacy/**", ignore: true }
    fallback_team: platform
  - name: acme/only-codeowners
    owners_source: codeowners
    modules:
      - { path: "services/**", team: payments }
  - name: acme/only-config
    owners_source: config
    modules:
      - { path: "libs/**", team: platform }
  - name: acme/orphan
`

const syntheticCodeowners = `
/services/billing/  @acme/sre
/libs/              @acme/platform
/libs/vendored/
/docs/*             @acme/payments
**/logs             @acme/sre
`

func TestResolverWithSyntheticConfig(t *testing.T) {
	cfg, err := config.Parse([]byte(syntheticYAML))
	if err != nil {
		t.Fatal(err)
	}
	co, err := ParseCodeowners(strings.NewReader(syntheticCodeowners))
	if err != nil {
		t.Fatal(err)
	}
	r := NewResolver(cfg, StaticCodeowners{
		"acme/monorepo":        co,
		"acme/only-codeowners": co,
		"acme/only-config":     co,
		"acme/orphan":          co,
	})

	cases := []struct {
		name, repo, path string
		want             want
	}{
		{
			name: "o glob mais longo de modules vence o mais curto",
			repo: "acme/monorepo", path: "services/billing/invoice.go",
			want: want{team: "payments", rule: "config:services/billing/**", linearTeam: "PAY", project: "Sentinela · Payments", triage: "Inbox", assignee: "maria@empresa.com", blocked: true},
		},
		{
			name: "módulo marcado como ignore fica fora da análise",
			repo: "acme/monorepo", path: "services/legacy/old.go",
			want: want{ignored: true, rule: "config:services/legacy/**"},
		},
		{
			name: "owners_source codeowners pula modules mesmo quando um glob casaria",
			repo: "acme/only-codeowners", path: "services/billing/invoice.go",
			want: want{team: "platform", rule: "codeowners:L2", triage: "Triage"},
		},
		{
			name: "owners_source config pula o CODEOWNERS e cai em defaults sem fallback",
			repo: "acme/only-config", path: "docs/readme.md",
			want: want{team: "", rule: "defaults", linearTeam: "PLAT", project: "Sentinela", triage: "Triage"},
		},
		{
			name: "caminho sem dono em lugar nenhum cai em defaults",
			repo: "acme/orphan", path: "cmd/main.go",
			want: want{team: "", rule: "defaults", linearTeam: "PLAT"},
		},
		{
			name: "regra do CODEOWNERS sem donos remove o dono e segue para o fallback",
			repo: "acme/monorepo", path: "libs/vendored/x.go",
			want: want{team: "platform", rule: "fallback"},
		},
		{
			name: "padrão docs/* não casa arquivos aninhados",
			repo: "acme/orphan", path: "docs/api/v1.md",
			want: want{team: "", rule: "defaults", linearTeam: "PLAT"},
		},
		{
			name: "padrão docs/* casa arquivo direto no diretório",
			repo: "acme/orphan", path: "docs/index.md",
			want: want{team: "payments", rule: "codeowners:L5", assignee: "maria@empresa.com", blocked: true},
		},
		{
			name: "padrão sem âncora casa diretório em qualquer profundidade",
			repo: "acme/orphan", path: "a/b/logs/out.txt",
			want: want{team: "platform", rule: "codeowners:L6"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Resolve(tc.repo, tc.path)
			if err != nil {
				t.Fatal(err)
			}
			check(t, got, tc.want)
		})
	}
}

func TestResolverRejectsUnknownRepo(t *testing.T) {
	cfg, co := loadFixtures(t)
	_, err := NewResolver(cfg, StaticCodeowners{monorepo: co}).Resolve("x/y", "a.go")
	if err == nil {
		t.Fatal("esperava erro para repo fora do sentinela.yaml")
	}
}

func TestPolicyMergesTeamOverDefaults(t *testing.T) {
	cfg, co := loadFixtures(t)
	got, err := NewResolver(cfg, StaticCodeowners{monorepo: co}).Resolve(monorepo, "README.md")
	if err != nil {
		t.Fatal(err)
	}
	// Primeiras execuções da Fase 4: nenhum time auto-aprova, nem a plataforma.
	if got.Policy.AutoApprove == nil || *got.Policy.AutoApprove {
		t.Error("plataforma deveria ter auto_approve false explícito")
	}
	if got.Policy.MaxLines == nil || *got.Policy.MaxLines != 50 {
		t.Error("max_lines deveria vir de defaults.policy")
	}
	if got.NotifySlack != "C0PLATFORM1" { // #sentinela-plataforma, canal próprio do time
		t.Errorf("notify = %q, quer C0PLATFORM1", got.NotifySlack)
	}
	exemplo, err := NewResolver(cfg, StaticCodeowners{}).Resolve("acme/app-java-angular", "backend/x.java")
	if err != nil || exemplo.NotifySlack != "C0DEFAULT01" { // sem canal próprio: defaults.notify.slack
		t.Errorf("notify do time-exemplo = %q (%v)", exemplo.NotifySlack, err)
	}
}

// Roteamento por achado (ADR 0017), com os casos reais da rodada 12 do monorepo-poliglota.
func TestResolveFindingPrefersSpecificRulesOverTheGenericOne(t *testing.T) {
	cfg, co := loadFixtures(t)
	r := NewResolver(cfg, StaticCodeowners{monorepo: co})
	cases := []struct {
		name string
		p    FindingPaths
		want want
	}{
		{
			name: "EX-14: evidência em go/cmd/api (curinga), escopo em go/internal/turmas",
			p:    FindingPaths{Primary: "go/cmd/api/main.go", Scope: []string{"go/cmd/api", "go/internal/turmas"}, Evidence: []string{"go/cmd/api/main.go"}},
			want: want{team: "academico", rule: "codeowners:L11 via scope:go/internal/turmas", project: "Sentinela · Acadêmico"},
		},
		{
			name: "EX-15: só go/cmd/api no escopo e na evidência; a correção cita turmas.ErrNaoEncontrada",
			p:    FindingPaths{Primary: "go/cmd/api/main.go", Scope: []string{"go/cmd/api"}, Evidence: []string{"go/cmd/api/main.go"}, Refs: []string{"go/internal/turmas"}},
			want: want{team: "academico", rule: "codeowners:L11 via referência:go/internal/turmas", project: "Sentinela · Acadêmico"},
		},
		{
			name: "EX-12: nada específico, fica na regra curinga",
			p:    FindingPaths{Primary: "go/cmd/api/main.go", Scope: []string{"go/cmd/api"}},
			want: want{team: "plataforma", rule: "codeowners:L7"},
		},
		{
			name: "escopo vence evidência",
			p:    FindingPaths{Primary: "README.md", Scope: []string{"go/internal/financeiro"}, Evidence: []string{"go/internal/matricula/m.go"}},
			want: want{team: "financeiro", rule: "codeowners:L21 via scope:go/internal/financeiro", assignee: "lead@example.com", blocked: true},
		},
		{
			name: "no escopo, vence o caminho mais específico",
			p:    FindingPaths{Primary: "README.md", Scope: []string{"go/archtest", "kotlin/src/main/kotlin/escola/mensalidades"}},
			want: want{team: "financeiro", rule: "codeowners:L22 via scope:kotlin/src/main/kotlin/escola/mensalidades", assignee: "lead@example.com", blocked: true},
		},
		{
			name: "evidência principal específica não muda, mesmo com escopo de outro time",
			p:    FindingPaths{Primary: "go/internal/matricula/m.go", Scope: []string{"go/internal/financeiro"}},
			want: want{team: "academico", rule: "codeowners:L10"},
		},
		{
			name: "fallback também é genérico (e o aviso do owner sem time continua)",
			p:    FindingPaths{Primary: "python/tests/test_x.py", Scope: []string{"python/escola/modules/turmas"}},
			want: want{team: "academico", rule: "codeowners:L18 via scope:python/escola/modules/turmas", warning: "@acme/qa"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.ResolveFinding(monorepo, tc.p)
			if err != nil {
				t.Fatal(err)
			}
			check(t, got, tc.want)
		})
	}
}
