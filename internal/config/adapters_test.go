package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func example(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "sentinela.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Sem adapters, vale o modo completo de antes: Linear pela API, Slack e as duas GitHub Apps.
func TestAdaptersDefaultToTheFullMode(t *testing.T) {
	cfg, err := Parse([]byte(example(t)))
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.Adapters
	if a.Tracker != TrackerLinearAPI || a.Notifier != NotifierSlackAPI || a.GitHost != GitHostGitHubApps || a.LocalDir != "reports/local" || cfg.LocalTracker() {
		t.Errorf("adapters = %+v", a)
	}
	if cfg.Labels.Finding != "sentinela" {
		t.Errorf("label de achado = %q", cfg.Labels.Finding)
	}
}

func TestAdaptersValidation(t *testing.T) {
	base := example(t)
	local := strings.Replace(base, "    team: PLAT                     # time do Linear usado quando nada mais casar\n", "", 1) +
		"\nadapters: { tracker: local, notifier: none, git_host: gh_cli }\n"
	if strings.Contains(local, "team: PLAT                     #") {
		t.Fatal("fixture: defaults.linear.team não saiu")
	}
	for _, tc := range []struct {
		name, yaml, want string
	}{
		{"local sem time do Linear", local, ""},
		{"linear_api sem time do Linear", strings.Replace(local, "tracker: local", "tracker: linear_api", 1), "defaults.linear.team é obrigatório"},
		{"tracker inválido", base + "\nadapters: { tracker: jira }\n", "adapters.tracker inválido"},
		{"notifier inválido", base + "\nadapters: { notifier: teams }\n", "adapters.notifier inválido"},
		{"git_host inválido", base + "\nadapters: { git_host: gitlab }\n", "adapters.git_host inválido"},
		{"gh_cli não aprova", strings.Replace(base, "reviewer_approves: false", "reviewer_approves: true", 1) + "\nadapters: { git_host: gh_cli }\n", "reviewer_approves não vale com adapters.git_host gh_cli"},
		{"label de time de outro sistema", strings.Replace(base, "    policy: { auto_approve: false, max_lines: 50 }", "    policy: { auto_approve: false, max_lines: 50 }\n    labels: { trigger: claude-fix }", 1), "teams.platform.labels.trigger"},
	} {
		_, err := Parse([]byte(tc.yaml))
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: erro %v, quer %q", tc.name, err, tc.want)
		}
	}
}

// Gatilho, done e failed trocam por time; o resto fica com a organização.
func TestLabelsForTeamOverridesTriggerDoneAndFailed(t *testing.T) {
	y := strings.Replace(example(t), "    policy: { auto_approve: false, max_lines: 50 }",
		"    policy: { auto_approve: false, max_lines: 50 }\n    labels: { trigger: corrigir, failed: falhou }", 1)
	cfg, err := Parse([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.LabelsFor("platform")
	if p.Trigger != "corrigir" || p.Failed != "falhou" || p.Done != "sentinela-done" || p.Question != "sentinela-pergunta" {
		t.Errorf("platform = %+v", p)
	}
	if o := cfg.LabelsFor("payments"); o.Trigger != "payments-fix" || o.Failed != "sentinela-failed" {
		t.Errorf("payments = %+v (troca só o gatilho, no exemplo)", o)
	}
	if o := cfg.LabelsFor("(padrão)"); o.Trigger != "sentinela-fix" {
		t.Errorf("time desconhecido = %+v", o)
	}
}

// Projeto do Linear: time > repo > defaults (último recurso), com a fonte.
func TestProjectForTeamThenRepoThenDefaults(t *testing.T) {
	cfg, err := Parse([]byte(example(t)))
	if err != nil {
		t.Fatal(err)
	}
	withProject := &Team{Linear: LinearTarget{Project: "Do time"}}
	rp := &Repo{Name: "acme/x", LinearProject: "Do repo"}
	bare := &Repo{Name: "acme/y"}
	for _, tc := range []struct {
		team       *Team
		rp         *Repo
		defaults   string
		want, from string
	}{
		{withProject, rp, "Padrão", "Do time", "teams.t.linear.project"},
		{&Team{}, rp, "Padrão", "Do repo", "repos[acme/x].linear_project"},
		{nil, bare, "Padrão", "Padrão", "defaults.linear.project"},
		{nil, bare, "", "", "nenhum (a issue sai sem projeto)"},
	} {
		cfg.Defaults.Linear.Project = tc.defaults
		if p, src := cfg.ProjectFor(tc.team, "t", tc.rp); p != tc.want || src != tc.from {
			t.Errorf("ProjectFor = %q de %q, quer %q de %q", p, src, tc.want, tc.from)
		}
	}
}

// O schema em schema/ é gerado do código: se uma chave mudar, `sentinela config schema` (e
// --team) precisa ser rodado de novo e o arquivo commitado.
func TestSchemaFilesAreUpToDate(t *testing.T) {
	for file, gen := range map[string]func() ([]byte, error){"sentinela.schema.json": Schema, "team.schema.json": TeamSchema} {
		want, err := gen()
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join("..", "..", "schema", file))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("schema/%s desatualizado: rode `go run ./cmd/sentinela config schema%s > schema/%s`", file, map[bool]string{true: " --team"}[file == "team.schema.json"], file)
		}
	}
}
