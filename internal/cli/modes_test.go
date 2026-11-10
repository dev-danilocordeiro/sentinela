package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pgvector/pgvector-go"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/github"
	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

func runWith(t *testing.T, env map[string]string, hooks *initHooks, args ...string) (string, error) {
	t.Helper()
	cmd := newRootCmd(func(k string) string { return env[k] }, hooks)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestLocalConfigTemplateIsValid(t *testing.T) {
	for _, repos := range [][]string{nil, {"acme/svc", "acme/web"}} {
		cfg, err := config.Parse([]byte(LocalConfig(repos)))
		if err != nil {
			t.Fatalf("repos %v: %v", repos, err)
		}
		a := cfg.Adapters
		if a.Tracker != config.TrackerLocal || a.Notifier != config.NotifierNone || a.GitHost != config.GitHostGHCLI || len(cfg.Repos) != len(repos) {
			t.Errorf("repos %v: adapters %+v, %d repos", repos, a, len(cfg.Repos))
		}
	}
}

type fakeInit struct {
	missing map[string]bool
	ran     []string
	connect error
	login   error
	claim   error  // resposta do registro do diretório no banco
	adopt   bool   // o init pediu para adotar o banco
	envAtUp bool   // o .env já existia quando o compose subiu
	claimed string // diretório registrado
	noLogin bool   // claude auth status diz que não há login
}

func (f *fakeInit) hooks() *initHooks {
	return &initHooks{
		LookPath: func(name string) (string, error) {
			if f.missing[name] {
				return "", errors.New("não achou")
			}
			return "/usr/bin/" + name, nil
		},
		Run: func(_ context.Context, name string, args ...string) (string, error) {
			f.ran = append(f.ran, name+" "+strings.Join(args, " "))
			if name == "docker" {
				_, err := os.Stat(".env")
				f.envAtUp = err == nil
			}
			return "", nil
		},
		ClaudeAuth: func(context.Context) (bool, string, error) {
			if f.noLogin {
				return false, "none", nil
			}
			return true, "claude.ai", nil
		},
		Claim: func(_ context.Context, _ string, dir string, adopt bool) error {
			f.adopt, f.claimed = adopt, dir
			return f.claim
		},
		Connect: func(context.Context, string) error { return f.connect },
		Migrate: func(context.Context, string) (int, error) { return 16, nil },
		GHLogin: func(context.Context) (string, error) {
			if f.login != nil {
				return "", f.login
			}
			return "dev-exemplo", nil
		},
	}
}

// init --mode local: sobe o Postgres, aplica as migrations, gera o yaml mínimo e o .env, confere
// o gh; numa segunda vez não mexe no que existe; o que falta vira lista e erro.
func TestInitLocalModePreparesEverythingAndListsWhatIsMissing(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("docker-compose.yml", []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeInit{missing: map[string]bool{"semgrep": true}}
	out, err := runWith(t, map[string]string{}, f.hooks(), "--config", "sentinela.yaml", "init", "--mode", "local", "--repo", "acme/svc")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	for _, want := range []string{"ok: Postgres no docker compose", "ok: conexão com o banco (postgres://sentinela:***@localhost:5432", "migrations aplicadas (16", "sentinela.yaml gerado", "gh logado como dev-exemplo", "opcional: scanners fora do PATH (a descoberta roda sem eles, com menos sinais): semgrep", "pronto."} {
		if !strings.Contains(out, want) {
			t.Errorf("saída sem %q:\n%s", want, out)
		}
	}
	if len(f.ran) != 1 || f.ran[0] != "docker compose up -d postgres" {
		t.Errorf("comandos = %v", f.ran)
	}
	cfg, err := config.Load("sentinela.yaml")
	if err != nil || !cfg.LocalTracker() || len(cfg.Repos) != 1 || cfg.Repos[0].Name != "acme/svc" {
		t.Fatalf("yaml gerado: %+v, %v", cfg, err)
	}
	env, _ := os.ReadFile(".env")
	if !strings.Contains(string(env), "DATABASE_URL="+DefaultLocalDatabaseURL) {
		t.Errorf(".env = %s", env)
	}

	// Segunda vez: nada é sobrescrito; sem gh logado e sem banco, lista o que falta.
	if err := os.WriteFile(".env", []byte("DATABASE_URL=outro\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f2 := &fakeInit{missing: map[string]bool{"claude": true}, connect: errors.New("connection refused"), login: errors.New("not logged in")}
	h := f2.hooks()
	out, err = runWith(t, map[string]string{}, h, "--config", "sentinela.yaml", "init")
	if !errors.Is(err, ErrInitIncomplete) {
		t.Fatalf("init sem banco e sem gh: %v\n%s", err, out)
	}
	for _, want := range []string{"já existe (não mexi): tracker local, notifier none, git_host gh_cli, 1 repo(s)", "falta: conexão com o banco", "falta: gh logado", "falta: Claude Code", "3 pendência(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("saída sem %q:\n%s", want, out)
		}
	}
	if b, _ := os.ReadFile(".env"); string(b) != "DATABASE_URL=outro\n" {
		t.Errorf(".env existente foi mexido: %s", b)
	}
	if _, err := runWith(t, map[string]string{}, h, "init", "--mode", "jira"); err == nil || !strings.Contains(err.Error(), "use local ou mcp") {
		t.Errorf("--mode mcp: %v", err)
	}
}

// config validate no modo local não chama o Linear nem confere Slack e GitHub Apps.
func TestConfigValidateInLocalModeSkipsLinearSlackAndApps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sentinela.yaml")
	if err := os.WriteFile(path, []byte(LocalConfig([]string{"acme/svc"})), 0o600); err != nil {
		t.Fatal(err)
	}
	lin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("o modo local chamou o Linear")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(lin.Close)
	env := envFor(lin, mockGitHub(t))
	env["SLACK_BOT_TOKEN"] = "xoxb-nao-usado"
	out, err := runWith(t, env, nil, "--config", path, "config", "validate")
	if err != nil {
		t.Fatalf("validate: %v\n%s", err, out)
	}
	for _, want := range []string{"adapters.tracker local", "adapters.notifier none", "adapters.git_host gh_cli", "0 erro(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("saída sem %q:\n%s", want, out)
		}
	}
}

// Os comandos de decisão pela CLI: aceita F-<id> ou o número, grava e regenera o relatório.
func TestFindingCommandsDecideAndWriteTheReport(t *testing.T) {
	url := os.Getenv("SENTINELA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SENTINELA_TEST_DATABASE_URL não definida")
	}
	ctx := context.Background()
	m, err := store.NewMigrator(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.DownTo(context.Background(), 0); _ = m.Close() })
	if _, err := m.DownTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	dir := t.TempDir()
	path := filepath.Join(dir, "eval.yaml")
	y := strings.Replace(LocalConfig([]string{"acme/svc"}), "local_dir: reports/local", "local_dir: "+filepath.Join(dir, "reports"), 1)
	if err := os.WriteFile(path, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, fp := range []string{"aaaaaaaaaaaa", "bbbbbbbbbbbb"} {
		routing, _ := json.Marshal(repo.Routing{Team: "dono", Rule: "repo"})
		id, err := st.Q.InsertFinding(ctx, db.InsertFindingParams{
			Fingerprint: fp, Source: "code_analysis", Repo: "acme/svc", Module: "cmd", Owners: []string{}, Routing: routing,
			Category: "bug", Severity: "medium", Confidence: 0.8, Title: "Achado " + fp, Evidence: []byte("[]"), Signals: []byte("[]"),
			AcceptanceCriteria: []string{}, ScopePaths: []string{"cmd"}, RelatedModules: []string{}, Embedding: pgvector.NewVector(make([]float32, 1024)),
			JudgeConfidence: pgtype.Float4{Float32: 0.7, Valid: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		ids[fp] = id
	}
	env := map[string]string{"DATABASE_URL": url}
	a, b := ids["aaaaaaaaaaaa"], ids["bbbbbbbbbbbb"]
	sa, sb := itoa(a), itoa(b)
	out, err := runWith(t, env, nil, "--config", path, "finding", "reject", "F-"+sa, "falso positivo", "--executed-by", "agent", "--note", "teste do modo local", "--by", "dono")
	if err != nil || !strings.Contains(out, "achado "+sa+" rejeitado (decided_by human, executed_by agent)") || !strings.Contains(out, "relatório: ") {
		t.Fatalf("reject: %v\n%s", err, out)
	}
	out, err = runWith(t, env, nil, "--config", path, "finding", "fix", sb, "--by", "dono")
	if err != nil || !strings.Contains(out, "branch sentinela/F-"+sb) {
		t.Fatalf("fix: %v\n%s", err, out)
	}
	out, err = runWith(t, env, nil, "--config", path, "finding", "list", "--all")
	if err != nil || !strings.Contains(out, "rejected") || !strings.Contains(out, "1 queued") {
		t.Errorf("list: %v\n%s", err, out)
	}
	out, err = runWith(t, env, nil, "--config", path, "finding", "show", sa)
	if err != nil || !strings.Contains(out, "decisão rejected") || !strings.Contains(out, "executed_by agent") || !strings.Contains(out, "(nota: teste do modo local)") {
		t.Errorf("show: %v\n%s", err, out)
	}
	md, err := os.ReadFile(filepath.Join(dir, "reports", "acme__svc.md"))
	if err != nil || !strings.Contains(string(md), "sentinela --config "+path+" finding") {
		t.Errorf("relatório: %v\n%s", err, md)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

const mcpList = `Checking MCP server health…

claude.ai Slack: https://mcp.slack.com/mcp - ✔ Connected
claude.ai Linear: https://mcp.linear.app/mcp - ✔ Connected
`

// init --mode mcp: além do local, confere o MCP do Linear no claude, descobre o time quando há um
// só e gera o yaml com linear_mcp (e slack_mcp com --slack-channel).
func TestInitMCPModeChecksTheLinearMCPAndWritesTheConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("docker-compose.yml", []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeInit{}
	h := f.hooks()
	run := h.Run
	h.Run = func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "claude" {
			return mcpList, nil
		}
		return run(ctx, name, args...)
	}
	h.LinearTeams = func(context.Context, string) ([]string, error) { return []string{"HAY"}, nil }
	out, err := runWith(t, map[string]string{"DATABASE_URL": "postgres://x"}, h, "--config", "sentinela.yaml", "init", "--mode", "mcp", "--repo", "acme/svc", "--slack-channel", "C0TESTE0001")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	for _, want := range []string{"ok: MCP do Linear conectado (claude.ai Linear)", "time do Linear: HAY (o único)", "MCP do Slack conectado", "tracker linear_mcp, notifier slack_mcp, git_host gh_cli", "modo sombra: grava os achados, sem publicar no Linear", "publica os pendentes no time HAY, como você"} {
		if !strings.Contains(out, want) {
			t.Errorf("saída sem %q:\n%s", want, out)
		}
	}
	cfg, err := config.Load("sentinela.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.LinearMCP() || cfg.Defaults.Linear.Team != "HAY" || cfg.Adapters.Notifier != config.NotifierSlackMCP || cfg.Defaults.Notify.Slack != "C0TESTE0001" || cfg.Adapters.MCP.Model != "claude-haiku-5-5" || cfg.Publish.Enabled {
		t.Errorf("config = %+v %+v", cfg.Adapters, cfg.Defaults)
	}
	if _, err := os.Stat(".env"); err == nil {
		t.Error("com DATABASE_URL no ambiente, o .env não deveria ser criado")
	}

	// Sem o MCP do Linear conectado: lista o que falta e não gera o yaml.
	t.Chdir(t.TempDir())
	_ = os.WriteFile("docker-compose.yml", []byte("services: {}\n"), 0o600)
	h.Run = func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "claude" {
			return "claude.ai Slack: https://mcp.slack.com/mcp - ✔ Connected\n", nil
		}
		return run(ctx, name, args...)
	}
	out, err = runWith(t, map[string]string{"DATABASE_URL": "postgres://x"}, h, "--config", "sentinela.yaml", "init", "--mode", "mcp")
	if !errors.Is(err, ErrInitIncomplete) || !strings.Contains(out, "falta: MCP do Linear conectado") || !strings.Contains(out, "sentinela.yaml não foi gerado") {
		t.Errorf("sem MCP: %v\n%s", err, out)
	}
	// Com pendências, a última linha manda rodar de novo o modo pedido, não o local.
	if !strings.Contains(out, "rode `sentinela init --mode mcp` de novo") || strings.Contains(out, "--mode local") {
		t.Errorf("a última linha deveria pedir --mode mcp:\n%s", out)
	}
}

type stubMCPTracker struct {
	linear.Tracker
	ws *linear.Workspace
}

func (s stubMCPTracker) Workspace(context.Context) (*linear.Workspace, error) { return s.ws, nil }

// config validate com linear_mcp: confere times, estados e labels pelo MCP e avisa a identidade.
func TestConfigValidateInMCPModeUsesTheMCPAndWarnsAboutIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sentinela.yaml")
	y := strings.Replace(MCPConfig([]string{"acme/svc"}, "HAY", ""), "    policy: { auto_approve: false }\n", "    policy: { auto_approve: false }\n    labels: { trigger: corrigir }\n", 1)
	if err := os.WriteFile(path, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &deps{env: func(k string) string { return map[string]string{"GITHUB_TOKEN": "gh_test"}[k] }, cfgPath: &path}
	d.mcp = stubMCPTracker{ws: &linear.Workspace{Teams: []linear.Team{{Key: "HAY", States: []string{"Triage", "Done"}, Labels: []string{"sentinela", "sentinela-fix", "sentinela-done", "sentinela-failed"}}}}}
	cfg, err := d.config()
	if err != nil {
		t.Fatal(err)
	}
	rep := ValidateRemote(context.Background(), cfg, d.tracker(cfg, nil), github.NewClient(mockGitHub(t).URL, "gh_test"), nil)
	all := strings.Join(append(append(rep.Info, rep.Warnings...), rep.Errors...), "\n")
	for _, want := range []string{"linear_mcp: issues e comentários saem como você", "teams.dono.labels.trigger: label \"corrigir\" não existe no time HAY"} {
		if !strings.Contains(all, want) {
			t.Errorf("relatório sem %q:\n%s", want, all)
		}
	}
}

// status no modo MCP: chamadas por ciclo de leitura e por dia, a partir de llm_calls.
func TestStatusShowsMCPCallsPerCycleAndPerDay(t *testing.T) {
	url := os.Getenv("SENTINELA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SENTINELA_TEST_DATABASE_URL não definida")
	}
	ctx := context.Background()
	m, err := store.NewMigrator(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.DownTo(context.Background(), 0); _ = m.Close() })
	if _, err := m.DownTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	for _, op := range []string{"poll", "poll", "get", "comment"} {
		if _, err := st.Q.InsertLLMCall(ctx, db.InsertLLMCallParams{
			Agent: "linear_mcp", Model: "claude-haiku-5-5", PromptVersion: "mcp", Mode: "cli", CustomID: pgtype.Text{String: op, Valid: true},
			Status: "succeeded", LatencyMs: 9000, Provider: "claude_code", CostUsd: store.NanoUSDToNumeric(0),
		}); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Parse([]byte(MCPConfig([]string{"acme/svc"}, "HAY", "")))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := printMCPCalls(ctx, &out, cfg, st); err != nil {
		t.Fatal(err)
	}
	if want := "MCP (7 dias): 4 chamada(s) claude -p, 4/dia; 2 ciclo(s) de leitura, 1.5 chamada(s) de leitura por ciclo; 9.0 s em média; 0 com erro"; !strings.Contains(out.String(), want) {
		t.Errorf("status = %q, quer %q", out.String(), want)
	}
}

// status: custo de 30 dias por repo (descoberta) e por time (correções), base para estimar o custo
// por time numa empresa.
func TestStatusShowsCostPerRepoAndTeam(t *testing.T) {
	url := os.Getenv("SENTINELA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SENTINELA_TEST_DATABASE_URL não definida")
	}
	ctx := context.Background()
	m, err := store.NewMigrator(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.DownTo(context.Background(), 0); _ = m.Close() })
	if _, err := m.DownTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	run, err := st.Q.CreateRun(ctx, db.CreateRunParams{Repo: "acme/svc", BudgetUsd: store.NanoUSDToNumeric(0), Mode: "full"})
	if err != nil {
		t.Fatal(err)
	}
	routing, _ := json.Marshal(map[string]string{"team": "pagamentos"})
	fid, err := st.Q.InsertFinding(ctx, db.InsertFindingParams{
		Fingerprint: "c0570000000a", Source: "code_analysis", Repo: "acme/svc", Module: "x", Owners: []string{}, Routing: routing,
		Category: "bug", Severity: "low", Confidence: 0.7, Title: "t", Evidence: []byte("[]"), Signals: []byte("[]"),
		AcceptanceCriteria: []string{}, ScopePaths: []string{}, RelatedModules: []string{}, Embedding: pgvector.NewVector(make([]float32, 1024)),
	})
	if err != nil {
		t.Fatal(err)
	}
	e, err := st.Q.EnqueueExecution(ctx, db.EnqueueExecutionParams{FindingID: fid, Kind: "fix", Repo: "acme/svc", IssueIdentifier: "EX-1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []db.InsertLLMCallParams{
		{RunID: pgtype.Int8{Int64: run.ID, Valid: true}, Agent: "judge", CostUsd: store.NanoUSDToNumeric(250_000_000)},
		{CustomID: pgtype.Text{String: "execution:" + itoa(e.ID), Valid: true}, Agent: "sentinela-fix", CostUsd: store.NanoUSDToNumeric(1_500_000_000)},
	} {
		c.Model, c.PromptVersion, c.Mode, c.Status, c.Provider = "m", "v", "sync", "succeeded", "anthropic_api"
		if _, err := st.Q.InsertLLMCall(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Parse([]byte(strings.Replace(LocalConfig([]string{"acme/svc"}), "provider: claude_code", "provider: anthropic_api", 1)))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := printCosts(ctx, &out, cfg, st); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"custo da descoberta por repo (30 dias, US$): acme/svc 0.25 (1 chamadas)", "custo das correções por time (30 dias, US$): pagamentos 1.50 (1 jobs)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status sem %q:\n%s", want, out.String())
		}
	}
}

// D-06 da revisão de prontidão: o init monta a URL com SENTINELA_PG_PORT, grava no .env um
// COMPOSE_PROJECT_NAME próprio do diretório antes de subir o compose (ou passa -p sem .env) e
// recusa um banco que já é de outra instalação.
func TestInitUsesItsOwnPortProjectAndDatabase(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("docker-compose.yml", []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeInit{}
	out, err := runWith(t, map[string]string{"SENTINELA_PG_PORT": "55432"}, f.hooks(), "--config", "sentinela.yaml", "init", "--mode", "local", "--repo", "acme/svc")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if !strings.Contains(out, "localhost:55432/sentinela") {
		t.Errorf("a URL não usa SENTINELA_PG_PORT:\n%s", out)
	}
	env, err := os.ReadFile(".env")
	if err != nil {
		t.Fatal(err)
	}
	project := composeProject(dir)
	if !strings.HasPrefix(project, "sentinela-") || project == "sentinela" {
		t.Fatalf("projeto do compose = %q", project)
	}
	for _, want := range []string{"DATABASE_URL=postgres://sentinela:sentinela@localhost:55432/sentinela?sslmode=disable\n", "SENTINELA_PG_PORT=55432\n", "COMPOSE_PROJECT_NAME=" + project + "\n"} {
		if !strings.Contains(string(env), want) {
			t.Errorf(".env sem %q:\n%s", want, env)
		}
	}
	if !f.envAtUp || len(f.ran) != 1 || f.ran[0] != "docker compose up -d postgres" {
		t.Errorf("o .env com o projeto precisa existir antes do compose: envAtUp %v, comandos %v", f.envAtUp, f.ran)
	}
	if f.claimed == "" || f.adopt {
		t.Errorf("registro do diretório: %q adopt %v", f.claimed, f.adopt)
	}

	// Sem .env (DATABASE_URL no ambiente), o compose recebe o projeto por -p.
	t.Chdir(t.TempDir())
	_ = os.WriteFile("docker-compose.yml", []byte("services: {}\n"), 0o600)
	g := &fakeInit{}
	if out, err := runWith(t, map[string]string{"DATABASE_URL": "postgres://x"}, g.hooks(), "--config", "sentinela.yaml", "init", "--mode", "local", "--repo", "acme/svc"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if len(g.ran) != 1 || !strings.HasPrefix(g.ran[0], "docker compose -p sentinela-") {
		t.Errorf("sem .env, o compose precisa do -p: %v", g.ran)
	}

	// Banco de outra instalação: recusa, com a mensagem do registro.
	t.Chdir(t.TempDir())
	_ = os.WriteFile("docker-compose.yml", []byte("services: {}\n"), 0o600)
	h := &fakeInit{claim: fmt.Errorf("%w: este banco é da instalação em /srv/a", store.ErrOtherInstallation)}
	out, err = runWith(t, map[string]string{}, h.hooks(), "--config", "sentinela.yaml", "init", "--mode", "local", "--repo", "acme/svc", "--adotar-banco")
	if err == nil || !strings.Contains(out+err.Error(), "da instalação em /srv/a") {
		t.Fatalf("banco de outra instalação deveria ser recusado: %v\n%s", err, out)
	}
	if !h.adopt {
		t.Error("--adotar-banco não chegou ao registro")
	}
	if _, err := os.Stat("sentinela.yaml"); err == nil {
		t.Error("com o banco recusado, o init não deveria gerar o sentinela.yaml")
	}
}

// D-18 da revisão de prontidão: o init confere o login do claude (claude auth status), não só se
// ele está no PATH; sem login, a descoberta e a correção falhariam no primeiro claude -p.
func TestInitChecksTheClaudeLogin(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("docker-compose.yml", []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeInit{}
	out, err := runWith(t, map[string]string{}, f.hooks(), "--config", "sentinela.yaml", "init", "--mode", "local", "--repo", "acme/svc")
	if err != nil || !strings.Contains(out, "ok: claude logado (claude.ai)") {
		t.Fatalf("com login: %v\n%s", err, out)
	}
	t.Chdir(t.TempDir())
	_ = os.WriteFile("docker-compose.yml", []byte("services: {}\n"), 0o600)
	g := &fakeInit{noLogin: true}
	out, err = runWith(t, map[string]string{}, g.hooks(), "--config", "sentinela.yaml", "init", "--mode", "local", "--repo", "acme/svc")
	if !errors.Is(err, ErrInitIncomplete) || !strings.Contains(out, "falta: login do Claude Code") {
		t.Fatalf("sem login deveria faltar o login: %v\n%s", err, out)
	}
}
