package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mainYAML = `
defaults:
  linear: { team: PLAT }
  notify: { slack: C0SENTINELA }
budget: { per_run_usd: 1, per_pr_usd: 1 }
models: { triage: m, analysis: m, architect: m, judge: m, curator: m, spec_writer: m, executor: m, reviewer_security: m, reviewer_code: m, reviewer_reliability: m, reviewer_domain: m, approver: m }
pricing: { models: { m: { input: 1, output: 1 } } }
blocked_paths: ["**/migrations/**"]
teams:
  plataforma:
    linear: { team: PLAT }
    policy: { auto_approve: true, max_lines: 50 }
repos:
  - name: acme/plataforma
    modules:
      - { path: "infra/**", team: plataforma }
    fallback_team: plataforma
`

func writeConfig(t *testing.T, teams map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sentinela.yaml"), []byte(mainYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "config.d", "teams"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, body := range teams {
		if err := os.WriteFile(filepath.Join(dir, "config.d", "teams", name+".yaml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "sentinela.yaml")
}

const pagamentosYAML = `
github: ["@acme/pagamentos"]
linear: { team: PAG, project: "Pagamentos", category_projects: { security: "Pagamentos · Segurança" } }
notify: { slack: C0PAGAMENT1, mention: S0PAGTEAM01 }
policy: { blocked: true }
labels: { trigger: corrigir }
modules:
  - { repo: acme/plataforma, path: "modules/cobranca/**", linear_project: "Pagamentos · Cobrança" }
repos:
  - { name: acme/pagamentos-web, linear_project: "Pagamentos · Web" }
blocked_paths: ["**/cartao/**"]
`

// config.d/teams/<time>.yaml junta ao principal: o time, os módulos nos repos compartilhados, os
// repos inteiros do time e caminhos bloqueados a mais. Sem config.d, nada muda.
func TestTeamFilesAreMergedIntoTheMainConfig(t *testing.T) {
	cfg, err := Load(writeConfig(t, map[string]string{"pagamentos": pagamentosYAML}))
	if err != nil {
		t.Fatal(err)
	}
	pag := cfg.Teams["pagamentos"]
	if pag == nil || pag.Linear.Team != "PAG" || pag.Notify.Mention != "S0PAGTEAM01" || pag.Labels.Trigger != "corrigir" || *pag.Policy.Blocked != true || pag.Policy.AutoApprove != nil {
		t.Fatalf("time = %+v", pag)
	}
	if cfg.TeamFile("pagamentos") != " (config.d/teams/pagamentos.yaml)" || cfg.TeamFile("plataforma") != "" {
		t.Errorf("fonte dos times: %q %q", cfg.TeamFile("pagamentos"), cfg.TeamFile("plataforma"))
	}
	mono, _ := cfg.Repo("acme/plataforma")
	if len(mono.Modules) != 2 || mono.Modules[1].Team != "pagamentos" || mono.Modules[1].LinearProject != "Pagamentos · Cobrança" || cfg.FileOf(mono.Modules[1]) != " (config.d/teams/pagamentos.yaml)" {
		t.Errorf("módulos = %+v", mono.Modules)
	}
	web, ok := cfg.Repo("acme/pagamentos-web")
	if !ok || web.Team != "pagamentos" || web.DefaultBranch != "main" {
		t.Errorf("repo do time = %+v", web)
	}
	if strings.Join(cfg.BlockedPaths, ",") != "**/migrations/**,**/cartao/**" {
		t.Errorf("blocked_paths = %v (o time só acrescenta)", cfg.BlockedPaths)
	}
	if !cfg.ModuleProject("pagamentos", "Pagamentos · Cobrança") {
		t.Error("projeto de módulo de arquivo de time não reconhecido")
	}
	// Sem config.d: o principal sozinho, como antes.
	if c, err := Load(writeConfig(t, nil)); err != nil || len(c.Teams) != 1 {
		t.Errorf("sem config.d: %v", err)
	}
}

// Política da organização só vale no principal; o mesmo módulo em dois times é erro; as
// mensagens dizem a chave, o arquivo e o porquê.
func TestTeamFilesRejectOrgPolicyAndConflicts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		teams map[string]string
		want  []string
	}{
		{"modelos no arquivo do time", map[string]string{"pagamentos": pagamentosYAML + "models: { analysis: m }\n"},
			[]string{`config.d/teams/pagamentos.yaml: chave "models" só vale no sentinela.yaml principal: modelos e custos são da organização`}},
		{"auto_approve no arquivo do time", map[string]string{"pagamentos": strings.Replace(pagamentosYAML, "policy: { blocked: true }", "policy: { blocked: true, auto_approve: true }", 1)},
			[]string{`config.d/teams/pagamentos.yaml: chave "policy.auto_approve" só vale no sentinela.yaml principal`}},
		{"adapters e reviewer_approves", map[string]string{"pagamentos": pagamentosYAML + "adapters: { tracker: local }\nreviewer_approves: true\n"},
			[]string{`chave "adapters" só vale no sentinela.yaml principal`, `chave "reviewer_approves" só vale no sentinela.yaml principal`}},
		{"módulo em dois times", map[string]string{"pagamentos": pagamentosYAML, "catalogo": "modules:\n  - { repo: acme/plataforma, path: \"modules/cobranca/**\" }\n"},
			[]string{`repos[acme/plataforma]: o módulo "modules/cobranca/**" está em dois times: catalogo (config.d/teams/catalogo.yaml) e pagamentos (config.d/teams/pagamentos.yaml)`}},
		{"time também no principal, com chaves do time", map[string]string{"plataforma": "linear: { team: PLAT }\n"},
			[]string{`config.d/teams/plataforma.yaml: o time "plataforma" já está em teams do sentinela.yaml com chaves do time; no principal, teams.plataforma só pode ter policy, models e fix_trigger`}},
		{"repo desconhecido", map[string]string{"catalogo": "modules:\n  - { repo: acme/outro, path: \"x/**\" }\n"},
			[]string{`config.d/teams/catalogo.yaml: modules: o repo acme/outro não está no sentinela.yaml`}},
		{"chave desconhecida", map[string]string{"catalogo": "canal: x\n"},
			[]string{`config.d/teams/catalogo.yaml:`, "field canal not found"}},
	} {
		_, err := Load(writeConfig(t, tc.teams))
		for _, w := range tc.want {
			if err == nil || !strings.Contains(err.Error(), w) {
				t.Errorf("%s: erro %v, quer %q", tc.name, err, w)
			}
		}
	}
}

// O principal dá a política da organização a um time declarado em config.d (teams.<time> só com
// policy e models); a do principal vence a do arquivo.
func TestMainConfigSetsOrgPolicyForATeamFile(t *testing.T) {
	path := writeConfig(t, map[string]string{"pagamentos": pagamentosYAML})
	main := strings.Replace(mainYAML, "teams:\n", "teams:\n  pagamentos:\n    policy: { auto_approve: false, blocked: true, max_lines: 30 }\n    models: { analysis: m }\n", 1)
	if err := os.WriteFile(path, []byte(main), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Teams["pagamentos"]
	if p.Linear.Team != "PAG" || p.Policy.AutoApprove == nil || *p.Policy.AutoApprove || *p.Policy.MaxLines != 30 || p.Models.Analysis != "m" {
		t.Errorf("time = %+v / %+v", p.Linear, p.Policy)
	}
}
