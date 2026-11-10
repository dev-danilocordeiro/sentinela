package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

const teamPagamentos = `linear: { team: PAG, project: "Pagamentos" }
notify: { slack: C0PAGAMENT1, mention: S0PAGTEAM01 }
modules:
  - { repo: acme/svc, path: "modules/cobranca/**", linear_project: "Pagamentos · Cobrança" }
`

// validate --offline --changed: mostra o roteamento dos caminhos dos arquivos mudados, antes (git)
// e depois (arquivos atuais), sem chamar ninguém.
func TestValidateOfflineChangedShowsRoutingBeforeAndAfter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sentinela.yaml")
	if err := os.WriteFile(path, []byte(LocalConfig([]string{"acme/svc"})), 0o600); err != nil {
		t.Fatal(err)
	}
	teams := filepath.Join(dir, "config.d", "teams")
	_ = os.MkdirAll(teams, 0o750)
	if err := os.WriteFile(filepath.Join(teams, "pagamentos.yaml"), []byte(teamPagamentos), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "base")
	// O PR muda o projeto do módulo e acrescenta um módulo.
	changed := strings.Replace(teamPagamentos, "Pagamentos · Cobrança", "Pagamentos · Faturas", 1) + "  - { repo: acme/svc, path: \"modules/carteira/**\" }\n"
	if err := os.WriteFile(filepath.Join(teams, "pagamentos.yaml"), []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runWith(t, map[string]string{}, nil, "--config", path, "config", "validate", "--offline", "--changed", "config.d/teams/pagamentos.yaml", "--base", "HEAD")
	if err != nil {
		t.Fatalf("validate: %v\n%s", err, out)
	}
	for _, want := range []string{
		"ok: time pagamentos em config.d/teams/pagamentos.yaml",
		"owners explain dos caminhos afetados (antes = HEAD, depois = arquivos atuais):",
		"acme/svc modules/cobranca/exemplo:\n    antes:  time pagamentos [config:modules/cobranca/**] · projeto Pagamentos · Cobrança",
		"depois: time pagamentos [config:modules/cobranca/**] · projeto Pagamentos · Faturas [repos[acme/svc].modules[modules/cobranca/**].linear_project (config.d/teams/pagamentos.yaml)] · canal C0PAGAMENT1 [teams.pagamentos.notify.slack (config.d/teams/pagamentos.yaml)] · menção S0PAGTEAM01",
		"acme/svc modules/carteira/exemplo:\n    antes:  time dono [repo]",
		"depois: time pagamentos [config:modules/carteira/**] · projeto Pagamentos [teams.pagamentos.linear.project (config.d/teams/pagamentos.yaml)]",
		"0 erro(s)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("saída sem %q:\n%s", want, out)
		}
	}
}

// A config relida a cada ciclo: inválida, fica a última válida e o aviso sai uma vez; corrigida,
// a nova vale.
func TestLiveConfigKeepsTheLastValidOneAndWarnsOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sentinela.yaml")
	if err := os.WriteFile(path, []byte(LocalConfig([]string{"acme/svc"})), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &deps{env: func(string) string { return "" }, cfgPath: &path}
	first, err := d.config()
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	live := newLiveConfig(d, first, &log)
	ctx := context.Background()
	if got, changed := live.Reload(ctx); changed || got == nil {
		t.Errorf("sem mudança: changed %v", changed)
	}
	if err := os.WriteFile(path, []byte("adapters: { tracker: jira }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if got, changed := live.Reload(ctx); changed || got != first {
			t.Errorf("config inválida trocou a válida")
		}
	}
	if n := strings.Count(log.String(), "ficou inválida"); n != 1 {
		t.Errorf("aviso de config inválida %d vezes (quer 1):\n%s", n, log.String())
	}
	if err := os.WriteFile(path, []byte(LocalConfig([]string{"acme/svc", "acme/web"})), 0o600); err != nil {
		t.Fatal(err)
	}
	got, changed := live.Reload(ctx)
	if !changed || len(got.Repos) != 2 || !strings.Contains(log.String(), "config válida de novo") {
		t.Errorf("config corrigida: changed %v, %d repos\n%s", changed, len(got.Repos), log.String())
	}
}
