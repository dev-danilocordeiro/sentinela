package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
)

var (
	rootConfig     = filepath.Join("..", "..", "testdata", "sentinela.yaml")
	codeownersFile = filepath.Join("..", "..", "testdata", "CODEOWNERS")
)

// mockLinear simula a API GraphQL com o time EX, sem estado Triage, como o workspace real.
func mockLinear(t *testing.T, projects []string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "lin_test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		page := func(xs ...string) map[string]any {
			out := make([]map[string]string, 0, len(xs))
			for _, x := range xs {
				out = append(out, map[string]string{"name": x})
			}
			return map[string]any{"nodes": out, "pageInfo": map[string]any{"hasNextPage": false}}
		}
		var data map[string]any
		switch {
		case strings.Contains(req.Query, "SentinelaViewer"):
			data = map[string]any{"viewer": map[string]any{"id": "user-1", "name": "Pessoa", "app": false}}
		case strings.Contains(req.Query, "SentinelaTeams"):
			data = map[string]any{"teams": map[string]any{
				"nodes":    []any{map[string]any{"id": "team-ex", "key": "EX", "name": "time-exemplo"}},
				"pageInfo": map[string]any{"hasNextPage": false},
			}}
		case strings.Contains(req.Query, "states("):
			data = map[string]any{"team": map[string]any{"states": page("Backlog", "Todo", "In Progress", "Done", "Canceled")}}
		case strings.Contains(req.Query, "projects("):
			data = map[string]any{"team": map[string]any{"projects": page(projects...)}}
		case strings.Contains(req.Query, "labels("):
			data = map[string]any{"team": map[string]any{"labels": page("sentinela", "sentinela-fix", "sentinela-done", "sentinela-failed")}}
		default:
			t.Errorf("consulta inesperada: %s", req.Query)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// mockGitHub: todos os repos existem, os times @acme/* não (são fictícios), o usuário
// fulano existe e só o monorepo-poliglota tem CODEOWNERS e AGENTS.md (na branch pedida).
func mockGitHub(t *testing.T) *httptest.Server {
	t.Helper()
	co, err := os.ReadFile(codeownersFile)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == "/repos/acme/monorepo-poliglota/contents/.github/CODEOWNERS":
			_, _ = w.Write(co)
		case p == "/repos/acme/monorepo-poliglota/contents/AGENTS.md" && r.URL.Query().Get("ref") != "":
			_, _ = w.Write([]byte(`{}`))
		case p == "/repos/acme/monorepo-poliglota/languages":
			_, _ = w.Write([]byte(`{"Go": 50000, "Java": 30000, "TypeScript": 20000, "Shell": 300}`))
		case strings.Contains(p, "/contents/"), strings.HasPrefix(p, "/orgs/"):
			http.NotFound(w, r)
		case strings.HasPrefix(p, "/repos/acme/"), p == "/users/fulano":
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func allProjects(t *testing.T) []string {
	t.Helper()
	cfg, err := config.Load(rootConfig)
	if err != nil {
		t.Fatal(err)
	}
	var ps []string
	for _, tm := range cfg.Teams {
		if tm.Linear.Project != "" {
			ps = append(ps, tm.Linear.Project)
		}
	}
	for _, r := range cfg.Repos {
		ps = append(ps, r.LinearProject)
	}
	return ps
}

func run(t *testing.T, env map[string]string, args ...string) (string, error) {
	t.Helper()
	cmd := NewRootCmd(func(k string) string { return env[k] })
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"--config", rootConfig}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func envFor(lin, gh *httptest.Server) map[string]string {
	return map[string]string{
		"LINEAR_API_URL": lin.URL, "LINEAR_API_KEY": "lin_test",
		"GITHUB_API_URL": gh.URL, "GITHUB_TOKEN": "gh_test",
	}
}

func TestConfigValidatePassesAndWarnsAboutUnmappedCodeownersOwner(t *testing.T) {
	env := envFor(mockLinear(t, allProjects(t)), mockGitHub(t))

	out, err := run(t, env, "config", "validate")
	if err != nil {
		t.Fatalf("validate falhou: %v\n%s", err, out)
	}
	for _, want := range []string{
		"owner @acme/qa do CODEOWNERS (L28 /python/tests/) sem time mapeado",
		"@acme/financeiro não existe no GitHub",
		"projeto efetivo: acme/go-servico-modular, time time-exemplo: go-servico-modular (de repos[acme/go-servico-modular].linear_project)",
		"0 erro(s)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("saída sem %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "@fulano não existe") {
		t.Errorf("usuário existente não deveria gerar aviso:\n%s", out)
	}
}

// Repo sem AGENTS.md nem CLAUDE.md na raiz da branch padrão: aviso no validate (a correção não
// roda nele), sem erro.
func TestConfigValidateWarnsAboutReposWithoutCommandsDoc(t *testing.T) {
	env := envFor(mockLinear(t, allProjects(t)), mockGitHub(t))
	out, err := run(t, env, "config", "validate")
	if err != nil {
		t.Fatalf("validate falhou: %v\n%s", err, out)
	}
	if !strings.Contains(out, "acme/go-servico-modular: sem AGENTS.md nem CLAUDE.md na raiz (main): a correção pelo Sentinela só roda depois que o arquivo existir") {
		t.Errorf("sem o aviso do go-servico-modular:\n%s", out)
	}
	if strings.Contains(out, "acme/monorepo-poliglota: sem AGENTS.md") {
		t.Errorf("monorepo-poliglota tem AGENTS.md e não deveria gerar aviso:\n%s", out)
	}
}

func TestConfigValidateFailsWhenLinearProjectIsMissing(t *testing.T) {
	projects := allProjects(t)
	var without []string
	for _, p := range projects {
		if p != "app-java-angular" {
			without = append(without, p)
		}
	}
	env := envFor(mockLinear(t, without), mockGitHub(t))

	out, err := run(t, env, "config", "validate")
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("esperava ErrValidation, veio %v\n%s", err, out)
	}
	if !strings.Contains(out, `projeto "app-java-angular" não existe no time EX`) {
		t.Errorf("saída sem o erro do projeto:\n%s", out)
	}
}

func TestConfigValidateFailsWhenTriageStateDoesNotExist(t *testing.T) {
	cfg, err := os.ReadFile(rootConfig)
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(cfg), "triage_state: Backlog", "triage_state: Triage", 1)
	path := filepath.Join(t.TempDir(), "sentinela.yaml")
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	env := envFor(mockLinear(t, allProjects(t)), mockGitHub(t))
	cmd := NewRootCmd(func(k string) string { return env[k] })
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--config", path, "config", "validate"})
	if err := cmd.Execute(); !errors.Is(err, ErrValidation) {
		t.Fatalf("esperava ErrValidation, veio %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), `estado "Triage" não existe no time EX`) {
		t.Errorf("saída sem o erro de estado:\n%s", out.String())
	}
}

func TestConfigValidateOfflineSkipsRemoteChecks(t *testing.T) {
	out, err := run(t, map[string]string{}, "config", "validate", "--offline")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "4 times, 11 repos") {
		t.Errorf("saída inesperada:\n%s", out)
	}
}

func TestOwnersExplainFetchesCodeownersFromGitHub(t *testing.T) {
	env := envFor(mockLinear(t, nil), mockGitHub(t))

	out, err := run(t, env, "owners", "explain", "acme/monorepo-poliglota", "java/src/main/resources/db/V1.sql")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"time:            plataforma", "regra:           codeowners:L26", "inscritos:       academico", "projeto:         Sentinela · Plataforma (de teams.plataforma.linear.project)"} {
		if !strings.Contains(out, want) {
			t.Errorf("saída sem %q:\n%s", want, out)
		}
	}
}

func TestOwnersExplainWithLocalCodeownersFile(t *testing.T) {
	out, err := run(t, map[string]string{}, "owners", "explain", "--codeowners", codeownersFile,
		"acme/monorepo-poliglota", "go/internal/financeiro/boleto.go")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "time:            financeiro") || !strings.Contains(out, "blocked=true") {
		t.Errorf("saída inesperada:\n%s", out)
	}
}

func TestConfigBootstrapGeneratesLoadableConfigFromCodeowners(t *testing.T) {
	env := envFor(mockLinear(t, nil), mockGitHub(t))

	out, err := run(t, env, "config", "bootstrap", "--repo", "acme/monorepo-poliglota")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	cfg, err := config.Parse([]byte(out))
	if err != nil {
		t.Fatalf("a config gerada não carrega: %v\n%s", err, out)
	}
	for _, team := range []string{"academico", "financeiro", "plataforma", "qa", "fulano"} {
		if _, ok := cfg.Teams[team]; !ok {
			t.Errorf("time %s ausente na config gerada", team)
		}
	}
	r, ok := cfg.Repo("acme/monorepo-poliglota")
	if !ok || r.FallbackTeam != "plataforma" || r.OwnersSource != config.OwnersCodeowners {
		t.Errorf("repo gerado = %+v", r)
	}
	if cfg.Defaults.Linear.Team != "EX" || cfg.Defaults.Linear.TriageState != "Backlog" {
		t.Errorf("defaults.linear = %+v", cfg.Defaults.Linear)
	}
	if !strings.Contains(out, "@fulano é um usuário") {
		t.Errorf("deveria sugerir mover o usuário para members:\n%s", out)
	}
	// discovery.scanners pelas linguagens do repo no GitHub (Go, Java e TypeScript).
	if want := []string{"golangci-lint", "gosec", "govulncheck", "semgrep", "osv-scanner", "eslint", "tsc"}; !slices.Equal(cfg.Discovery.Scanners, want) {
		t.Errorf("discovery.scanners = %v, quer %v", cfg.Discovery.Scanners, want)
	}
	if !strings.Contains(out, "# - discovery.scanners pelas linguagens de acme/monorepo-poliglota no GitHub: go, java, typescript; semgrep com p/default e p/java, p/typescript, p/react, p/nodejs, p/expressjs") {
		t.Errorf("saída sem a nota das linguagens:\n%s", out)
	}
}

func TestConfigBootstrapRefusesToOverwriteExistingFile(t *testing.T) {
	env := envFor(mockLinear(t, nil), mockGitHub(t))
	existing := filepath.Join(t.TempDir(), "out.yaml")
	if err := os.WriteFile(existing, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, env, "config", "bootstrap", "--repo", "acme/monorepo-poliglota", "-o", existing); err == nil {
		t.Fatal("deveria recusar sobrescrever")
	}
}

func mockSlack(t *testing.T, members map[string]bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		ch := r.Form.Get("channel")
		w.Header().Set("Content-Type", "application/json")
		member, ok := members[ch]
		if r.URL.Path != "/conversations.info" || !ok {
			_, _ = w.Write([]byte(`{"ok":false,"error":"channel_not_found"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": map[string]any{"id": ch, "name": "c" + ch, "is_member": member}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestConfigValidateChecksEverySlackChannelTheBotMustPostTo(t *testing.T) {
	env := envFor(mockLinear(t, allProjects(t)), mockGitHub(t))
	env["SLACK_BOT_TOKEN"] = "xoxb-test"
	env["SLACK_API_URL"] = mockSlack(t, map[string]bool{
		"C0DEFAULT01": true, "C0ACADEMIC1": true, "C0FINANCE01": false, // C0PLATFORM1 não existe
	}).URL
	out, err := run(t, env, "config", "validate")
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("esperava ErrValidation, veio %v\n%s", err, out)
	}
	for _, want := range []string{
		"teams.financeiro.notify: o bot não está no canal C0FINANCE01",
		"teams.plataforma.notify: canal C0PLATFORM1: slack conversations.info: channel_not_found",
		"2 erro(s)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("saída sem %q:\n%s", want, out)
		}
	}
	for _, l := range strings.Split(out, "\n") {
		if (strings.HasPrefix(l, "erro:") || strings.HasPrefix(l, "aviso:")) && (strings.Contains(l, "C0DEFAULT01") || strings.Contains(l, "C0ACADEMIC1")) {
			t.Errorf("canais ok não deveriam aparecer em erro ou aviso: %s", l)
		}
	}
}

func TestConfigModelsShowsTheModelOfEachRoleAndWhereItCameFrom(t *testing.T) {
	out, err := run(t, map[string]string{}, "config", "models", "--repo", "acme/monorepo-poliglota", "--team", "academico")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"triage", "claude-haiku-5-5", "executor", "claude-opus-5-5", "models"} {
		if !strings.Contains(out, want) {
			t.Errorf("saída sem %q:\n%s", want, out)
		}
	}
	if _, err := run(t, map[string]string{}, "config", "models", "--team", "inexistente"); err == nil {
		t.Error("time fora do sentinela.yaml deveria falhar")
	}
}

// scan exige um repo ou --all, nunca os dois; a checagem vem antes de abrir o banco.
func TestScanNeedsRepoOrAll(t *testing.T) {
	for _, args := range [][]string{{"scan"}, {"scan", "--all", "--repo", "acme/x"}} {
		if _, err := run(t, map[string]string{}, args...); err == nil || !strings.Contains(err.Error(), "--repo R ou --all") {
			t.Errorf("%v: erro = %v", args, err)
		}
	}
}
