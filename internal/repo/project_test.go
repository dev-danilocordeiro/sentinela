package repo

import (
	"strings"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
)

const projectYAML = `
defaults:
  linear: { team: PLAT, project: "Padrão" }
  notify: { slack: C0DEFAULT01 }
budget: { per_run_usd: 1, per_pr_usd: 1 }
models: { triage: m, analysis: m, architect: m, judge: m, curator: m, spec_writer: m, executor: m, reviewer_security: m, reviewer_code: m, reviewer_reliability: m, reviewer_domain: m, approver: m }
pricing: { models: { m: { input: 1, output: 1 } } }
teams:
  pagamentos:
    linear: { team: PAG, project: "Pagamentos", category_projects: { security: "Pagamentos · Segurança" } }
    notify: { slack: C0PAGAMENT1, mention: S0PAGTEAM01 }
  catalogo:
    linear: { team: CAT }
  plataforma:
    linear: { team: PLAT }
repos:
  - name: acme/plataforma
    linear_project: "Monorepo"
    modules:
      - { path: "modules/cobranca/**", team: pagamentos, linear_project: "Pagamentos · Cobrança" }
      - { path: "modules/carteira/**", team: pagamentos }
      - { path: "modules/vitrine/**", team: catalogo }
    fallback_team: plataforma
  - name: acme/solo
    team: plataforma
`

// Projeto: módulo > time > repo > defaults no roteamento; categoria do time entra na publicação
// e perde para o módulo. Canal e menção com a fonte.
func TestProjectPrecedenceModuleTeamRepoDefaults(t *testing.T) {
	cfg, err := config.Parse([]byte(projectYAML))
	if err != nil {
		t.Fatal(err)
	}
	r := NewResolver(cfg, StaticCodeowners{})
	for _, tc := range []struct {
		repo, path, project, source string
	}{
		{"acme/plataforma", "modules/cobranca/fatura.go", "Pagamentos · Cobrança", "repos[acme/plataforma].modules[modules/cobranca/**].linear_project"},
		{"acme/plataforma", "modules/carteira/saldo.go", "Pagamentos", "teams.pagamentos.linear.project"},
		{"acme/plataforma", "modules/vitrine/lista.go", "Monorepo", "repos[acme/plataforma].linear_project"},
		{"acme/solo", "main.go", "Padrão", "defaults.linear.project"},
	} {
		res, err := r.Resolve(tc.repo, tc.path)
		if err != nil || res.LinearProject != tc.project || res.ProjectSource != tc.source {
			t.Errorf("%s: projeto %q de %q (%v), quer %q de %q", tc.path, res.LinearProject, res.ProjectSource, err, tc.project, tc.source)
		}
	}
	res, _ := r.Resolve("acme/plataforma", "modules/carteira/saldo.go")
	if res.NotifySlack != "C0PAGAMENT1" || res.NotifySource != "teams.pagamentos.notify.slack" || res.Mention != "S0PAGTEAM01" || res.MentionSource != "teams.pagamentos.notify.mention" {
		t.Errorf("aviso = %q (%s), menção %q (%s)", res.NotifySlack, res.NotifySource, res.Mention, res.MentionSource)
	}
	if strings.Join(res.CategoryProjects, ",") != "security -> Pagamentos · Segurança" {
		t.Errorf("por categoria = %v", res.CategoryProjects)
	}
	res, _ = r.Resolve("acme/plataforma", "modules/vitrine/lista.go")
	if res.NotifySource != "defaults.notify.slack" || res.Mention != "" || res.MentionSource != "nenhuma" {
		t.Errorf("catálogo: aviso %s, menção %q (%s)", res.NotifySource, res.Mention, res.MentionSource)
	}

	// Publicação: módulo > categoria > gravado > defaults.
	for _, tc := range []struct {
		team, category, stored, want string
	}{
		{"pagamentos", "security", "Pagamentos · Cobrança", "Pagamentos · Cobrança"}, // módulo vence a categoria
		{"pagamentos", "security", "Pagamentos", "Pagamentos · Segurança"},           // categoria vence o time
		{"pagamentos", "bug", "Pagamentos", "Pagamentos"},
		{"catalogo", "security", "Monorepo", "Monorepo"},
		{"catalogo", "bug", "", "Padrão"}, // gravado sem projeto: defaults
	} {
		if got := cfg.PublishProject(tc.team, tc.category, tc.stored); got != tc.want {
			t.Errorf("PublishProject(%s, %s, %q) = %q, quer %q", tc.team, tc.category, tc.stored, got, tc.want)
		}
	}
}

func TestCategoryProjectsValidation(t *testing.T) {
	for _, tc := range []struct{ from, to, want string }{
		{`category_projects: { security: "Pagamentos · Segurança" }`, `category_projects: { seguranca: "X" }`, `categoria "seguranca" não existe`},
		{`linear: { team: PLAT, project: "Padrão" }`, `linear: { team: PLAT, project: "Padrão", category_projects: { bug: "X" } }`, "defaults.linear.category_projects"},
		{`{ path: "modules/vitrine/**", team: catalogo }`, `{ path: "modules/vitrine/**", ignore: true, linear_project: "X" }`, "módulo ignorado"},
		{`project: "Pagamentos", category_projects`, `project: "Pagamentos · Cobrança", category_projects`, "é também o projeto de um módulo do time"},
	} {
		y := strings.Replace(projectYAML, tc.from, tc.to, 1)
		if y == projectYAML {
			t.Fatalf("fixture: %q não achado", tc.from)
		}
		if _, err := config.Parse([]byte(y)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.want, err)
		}
	}
}

// override: um módulo transversal vence os comuns, qualquer que seja o tamanho do glob; sem ele,
// vale o glob mais longo (ADR 0043).
func TestModuleOverrideBeatsLongerGlobs(t *testing.T) {
	y := strings.Replace(projectYAML, `      - { path: "modules/vitrine/**", team: catalogo }`, `      - { path: "modules/vitrine/**", team: catalogo }
      - { path: "**/Dockerfile", team: plataforma, override: true }
      - { path: "**/*.md", team: plataforma }`, 1)
	cfg, err := config.Parse([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	r := NewResolver(cfg, StaticCodeowners{})
	for path, team := range map[string]string{
		"modules/cobranca/Dockerfile": "plataforma", // override vence o glob mais longo
		"modules/cobranca/LEIAME.md":  "pagamentos", // sem override, vence o mais longo
		"Dockerfile":                  "plataforma",
	} {
		if res, _ := r.Resolve("acme/plataforma", path); res.Team != team {
			t.Errorf("%s: time %s (%s), quer %s", path, res.Team, res.Rule, team)
		}
	}
}
