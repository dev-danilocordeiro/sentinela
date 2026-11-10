package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
	"github.com/dev-danilocordeiro/sentinela/internal/mcpcall"
	"github.com/dev-danilocordeiro/sentinela/internal/scan"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
)

// DefaultLocalDatabaseURL é o Postgres do docker compose do projeto, na porta padrão.
const DefaultLocalDatabaseURL = "postgres://sentinela:sentinela@localhost:5432/sentinela?sslmode=disable" //nolint:gosec // senha de desenvolvimento do docker-compose.yml

// localDatabaseURL é o Postgres do docker compose na porta dada (SENTINELA_PG_PORT, a mesma que o
// docker-compose.yml publica).
func localDatabaseURL(port string) string {
	return strings.Replace(DefaultLocalDatabaseURL, "localhost:5432", "localhost:"+port, 1)
}

// composeProject é o nome do projeto do docker compose para o diretório da instalação: o nome
// padrão do compose é o do diretório, e dois clones chamados sentinela dividiriam o mesmo
// Postgres e o mesmo volume (D-06 da revisão de prontidão).
func composeProject(dir string) string {
	return "sentinela-" + config.InstallKey(dir)
}

// initHooks são as pontas externas do init; os testes trocam por falsos.
type initHooks struct {
	LookPath func(string) (string, error)
	// Run roda um comando e devolve a saída combinada.
	Run     func(ctx context.Context, name string, args ...string) (string, error)
	Connect func(ctx context.Context, url string) error
	Migrate func(ctx context.Context, url string) (int, error)
	// ClaudeAuth diz se o claude tem login e por qual método (claude auth status --json), com o
	// mesmo ambiente fechado das sessões.
	ClaudeAuth func(ctx context.Context) (loggedIn bool, method string, err error)
	// Claim confere e registra o diretório da instalação no banco (store.ClaimHome).
	Claim   func(ctx context.Context, url, dir string, adopt bool) error
	GHLogin func(ctx context.Context) (string, error)
	// LinearTeams lista as chaves dos times pelo MCP do Linear (init --mode mcp sem --linear-team).
	LinearTeams func(ctx context.Context, server string) ([]string, error)
	// Languages são as linguagens de um repo pelo GitHub (gh api repos/{repo}/languages), para a
	// lista discovery.scanners; nil não pergunta.
	Languages func(ctx context.Context, fullName string) (map[string]bool, error)
	Wait      time.Duration // quanto esperar o Postgres subir
}

func (d *deps) initHooks() initHooks {
	if d.hooks != nil {
		return *d.hooks
	}
	return initHooks{
		LookPath: exec.LookPath,
		Run: func(ctx context.Context, name string, args ...string) (string, error) {
			out, err := exec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec // comandos fixos do init
			return strings.TrimSpace(string(out)), err
		},
		Connect: func(ctx context.Context, url string) error {
			st, err := store.Open(ctx, url)
			if err != nil {
				return err
			}
			defer st.Close()
			return st.Pool.Ping(ctx)
		},
		Migrate: func(ctx context.Context, url string) (int, error) {
			m, err := store.NewMigrator(url)
			if err != nil {
				return 0, err
			}
			defer func() { _ = m.Close() }()
			res, err := m.Up(ctx)
			return len(res), err
		},
		ClaudeAuth: claudeAuthStatus,
		Claim: func(ctx context.Context, url, dir string, adopt bool) error {
			st, err := store.Open(ctx, url)
			if err != nil {
				return err
			}
			defer st.Close()
			return store.ClaimHome(ctx, st.Q, dir, adopt)
		},
		GHLogin:     d.ghcli().Login,
		LinearTeams: listLinearTeams,
		Languages: func(ctx context.Context, fullName string) (map[string]bool, error) {
			return ghLanguages(ctx, func(ctx context.Context, name string, args ...string) (string, error) {
				out, err := exec.CommandContext(ctx, name, args...).Output() //nolint:gosec // comando fixo do init
				return strings.TrimSpace(string(out)), err
			}, fullName)
		},
		Wait: 60 * time.Second,
	}
}

// listLinearTeams pergunta ao MCP do Linear as chaves dos times (uma chamada `claude -p`).
func listLinearTeams(ctx context.Context, server string) ([]string, error) {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".cache", "sentinela", "claude-cwd")
	_ = os.MkdirAll(dir, 0o750)
	r := &mcpcall.CLI{Model: "claude-haiku-5-5", WorkDir: dir, Timeout: 3 * time.Minute}
	var out struct {
		Teams []string `json:"teams"`
	}
	err := r.Run(ctx, mcpcall.Call{Agent: "linear_mcp", Server: server,
		Schema: map[string]any{"type": "object", "required": []string{"teams"}, "properties": map[string]any{"teams": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}},
		Prompt: mcpcall.Prompt("listar as chaves dos times do Linear (ex.: ENG)", mcpcall.Step{Tool: mcpcall.ToolPrefix(server) + "__list_teams", Args: map[string]any{}, Note: "Devolva em teams a chave (key) de cada time."})}, &out)
	return out.Teams, err
}

// initOpts são as escolhas do init.
type initOpts struct {
	mode, linearTeam, slackChannel, models string
	adopt                                  bool
	repos                                  []string
}

// ErrInitIncomplete: o init terminou com pendências obrigatórias.
var ErrInitIncomplete = errors.New("init incompleto: resolva o que falta e rode de novo")

func newInitCmd(d *deps) *cobra.Command {
	var o initOpts
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Prepara uma instalação: Postgres, migrations, sentinela.yaml mínimo e o que falta (--mode local | mcp)",
		Long: "--mode local (ADR 0038): sobe o Postgres do docker compose, aplica as migrations, confere a\n" +
			"conexão, gera um sentinela.yaml mínimo (tracker local, notifier none, git_host gh_cli) se ele\n" +
			"não existir, confere se o gh está logado e lista o que falta. Não cria nada fora da máquina.\n" +
			"--mode mcp (ADR 0039): o mesmo, com tracker linear_mcp (e notifier slack_mcp com\n" +
			"--slack-channel), e confere se o claude tem o MCP do Linear (e o do Slack) conectado.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if o.mode != "local" && o.mode != "mcp" {
				return fmt.Errorf("--mode %q não existe: use local ou mcp", o.mode)
			}
			if _, err := config.Profile(o.models); err != nil {
				return err
			}
			return runInit(c.Context(), d, c.OutOrStdout(), o)
		},
	}
	cmd.Flags().StringVar(&o.mode, "mode", "local", "modo de operação: local ou mcp")
	cmd.Flags().StringSliceVar(&o.repos, "repo", nil, "repo owner/nome a analisar (repita para vários)")
	cmd.Flags().StringVar(&o.linearTeam, "linear-team", "", "--mode mcp: chave do time do Linear (padrão: o único time, se houver um só)")
	cmd.Flags().StringVar(&o.models, "models", config.DefaultProfile, "perfil de modelos do sentinela.yaml gerado: qualidade, economia ou personalizado (bloco completo para editar)")
	cmd.Flags().BoolVar(&o.adopt, "adotar-banco", false, "registra este diretório num banco que já tem histórico e nenhum diretório registrado (instalação anterior à migration 00019)")
	cmd.Flags().StringVar(&o.slackChannel, "slack-channel", "", "--mode mcp: canal do Slack para os avisos pelo MCP (sem ele, notifier none)")
	return cmd
}

func runInit(ctx context.Context, d *deps, w io.Writer, o initOpts) error {
	repos := o.repos
	h := d.initHooks()
	var missing []string
	ok := func(format string, args ...any) { fmt.Fprintf(w, "ok: "+format+"\n", args...) }
	miss := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		missing = append(missing, msg)
		fmt.Fprintf(w, "falta: %s\n", msg)
	}

	// 1. Postgres do docker compose, na porta e no projeto desta instalação. O .env sai antes do
	// compose: ele lê dali o COMPOSE_PROJECT_NAME e o SENTINELA_PG_PORT.
	dir, err := store.HomeDir()
	if err != nil {
		return err
	}
	port := d.env("SENTINELA_PG_PORT")
	if port == "" {
		port = "5432"
	}
	url := d.env("DATABASE_URL")
	if url == "" {
		url = localDatabaseURL(port)
	}
	project := composeProject(dir)
	if err := writeEnvIfMissing(".env", url, port, project, d.env("DATABASE_URL") == ""); err != nil {
		miss(".env: %v", err)
	}
	compose := []string{"compose"}
	if _, err := os.Stat(".env"); err != nil && d.env("COMPOSE_PROJECT_NAME") == "" {
		// Sem .env (DATABASE_URL veio do ambiente): o projeto vai na linha de comando. Com um .env
		// que já existia, vale o que ele diz (ou o nome do diretório, como antes).
		compose = append(compose, "-p", project)
	}
	compose = append(compose, "up", "-d", "postgres")
	if _, err := os.Stat("docker-compose.yml"); err != nil {
		miss("docker-compose.yml do Sentinela no diretório atual (rode o init na raiz do repo do Sentinela)")
	} else if _, err := h.LookPath("docker"); err != nil {
		miss("docker (o Postgres com pgvector roda no docker compose)")
	} else if out, err := h.Run(ctx, "docker", compose...); err != nil {
		miss("subir o Postgres: docker %s: %v %s", strings.Join(compose, " "), err, out)
	} else {
		ok("Postgres no docker compose (docker %s)", strings.Join(compose, " "))
	}
	dbOK := false
	deadline := time.Now().Add(h.Wait)
	for {
		err := h.Connect(ctx, url)
		if err == nil {
			dbOK = true
			ok("conexão com o banco (%s)", redactURL(url))
			break
		}
		if time.Now().After(deadline) {
			miss("conexão com o banco %s: %v", redactURL(url), err)
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if dbOK {
		if n, err := h.Migrate(ctx, url); err != nil {
			miss("migrations: %v", err)
		} else {
			ok("migrations aplicadas (%d nova(s))", n)
			// Um banco de outra instalação não é usado: o init para aqui, sem gerar nada.
			if err := h.Claim(ctx, url, dir, o.adopt); err != nil {
				return fmt.Errorf("banco %s: %w", redactURL(url), err)
			}
			ok("banco desta instalação (diretório registrado no banco)")
		}
	}

	// 2. Modo mcp: o claude precisa ter os MCPs conectados (claude mcp list).
	const linearServer, slackServer = "claude.ai Linear", "claude.ai Slack"
	if o.mode == "mcp" {
		out, err := h.Run(ctx, "claude", "mcp", "list")
		switch {
		case err != nil:
			miss("listar os MCPs do Claude Code (claude mcp list): %v", err)
		case !mcpConnected(out, linearServer):
			miss("MCP do Linear conectado no Claude Code (%q em `claude mcp list`): conecte o Linear em claude.ai > Settings > Connectors ou com `claude mcp add`", linearServer)
		default:
			ok("MCP do Linear conectado (%s)", linearServer)
			if o.linearTeam == "" {
				teams, err := h.LinearTeams(ctx, linearServer)
				switch {
				case err != nil:
					miss("listar os times do Linear pelo MCP: %v", err)
				case len(teams) == 1:
					o.linearTeam = teams[0]
					ok("time do Linear: %s (o único)", o.linearTeam)
				default:
					miss("--linear-team (times: %s)", strings.Join(teams, ", "))
				}
			}
		}
		if o.slackChannel != "" && err == nil {
			if mcpConnected(out, slackServer) {
				ok("MCP do Slack conectado (%s): avisos em %s", slackServer, o.slackChannel)
			} else {
				miss("MCP do Slack conectado no Claude Code (%q), pedido por --slack-channel", slackServer)
			}
		}
	}

	// 3. sentinela.yaml mínimo.
	path := *d.cfgPath
	if _, err := os.Stat(path); err == nil {
		cfg, err := config.Load(path)
		if err != nil {
			miss("%s existe mas não valida: %v", path, err)
		} else {
			ok("%s já existe (não mexi): tracker %s, notifier %s, git_host %s, %d repo(s)", path, cfg.Adapters.Tracker, cfg.Adapters.Notifier, cfg.Adapters.GitHost, len(cfg.Repos))
			if len(cfg.Repos) == 0 {
				miss("repos no %s", path)
			}
		}
	} else if o.mode == "mcp" && o.linearTeam == "" {
		miss("%s não foi gerado: falta o time do Linear", path)
	} else {
		models := o.models
		if models == "" {
			models = config.DefaultProfile
		}
		prof, err := config.Profile(models)
		if err != nil {
			return err
		}
		plan := planScanners(repos, detectLanguages(ctx, h, w, repos))
		data := localConfig(repos, prof, plan)
		if o.mode == "mcp" {
			data = mcpConfig(repos, o.linearTeam, o.slackChannel, prof, plan)
		}
		if _, err := config.Parse([]byte(data)); err != nil {
			return fmt.Errorf("modelo do sentinela.yaml inválido: %w", err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			return err
		}
		cfg, _ := config.Parse([]byte(data))
		ok("%s gerado (tracker %s, notifier %s, git_host %s, modelos: perfil %s)", path, cfg.Adapters.Tracker, cfg.Adapters.Notifier, cfg.Adapters.GitHost, prof.Name)
		if len(repos) == 0 {
			miss("repos no %s (edite a lista repos ou rode de novo com --repo owner/nome)", path)
		}
	}

	// 4. Ferramentas.
	if _, err := h.LookPath("gh"); err != nil {
		miss("GitHub CLI (gh): https://cli.github.com, depois `gh auth login`")
	} else if login, err := h.GHLogin(ctx); err != nil {
		miss("gh logado: rode `gh auth login` (%v)", err)
	} else {
		ok("gh logado como %s (PRs em draft saem por essa conta)", login)
	}
	if _, err := h.LookPath("claude"); err != nil {
		miss("Claude Code (claude) logado na assinatura: https://docs.claude.com/claude-code")
	} else if logged, method, err := h.ClaudeAuth(ctx); err != nil {
		miss("login do Claude Code: claude auth status: %v", err)
	} else if !logged {
		miss("login do Claude Code: rode `claude auth login` (assinatura) ou `claude setup-token`; sem login, a descoberta e a correção falham no primeiro claude -p")
	} else {
		ok("claude logado (%s)", method)
	}
	if _, err := h.LookPath("git"); err != nil {
		miss("git")
	}
	var optional []string
	for _, s := range []string{"golangci-lint", "gosec", "govulncheck", "semgrep"} {
		if _, err := h.LookPath(s); err != nil {
			optional = append(optional, s)
		}
	}
	if _, err := h.LookPath("node"); err != nil {
		optional = append(optional, "node (eslint e tsc)")
	} else if _, err := os.Stat(filepath.Join("tools", "node", "node_modules", "eslint")); err != nil {
		fmt.Fprintf(w, "opcional: eslint e tsc precisam das ferramentas de Node do Sentinela: `npm ci --ignore-scripts --prefix tools/node` (ADR 0060)\n")
	}
	if len(optional) > 0 {
		fmt.Fprintf(w, "opcional: scanners fora do PATH (a descoberta roda sem eles, com menos sinais): %s\n", strings.Join(optional, ", "))
	}

	if len(missing) > 0 {
		fmt.Fprintf(w, "\n%d pendência(s). Depois de resolver, rode `sentinela init --mode %s` de novo.\n", len(missing), o.mode)
		return ErrInitIncomplete
	}
	reportDir := "reports/local"
	if cfg, err := config.Load(path); err == nil {
		reportDir = cfg.Adapters.LocalDir
	}
	flag := ""
	if path != "sentinela.yaml" {
		flag = " --config " + path
	}
	if cfg, err := config.Load(path); err == nil && cfg.LinearMCP() {
		if !cfg.Publish.Enabled {
			// Modo sombra (D-15): a primeira rodada só grava; publicar no Linear da empresa é um
			// passo explícito, depois de revisar o que sairia.
			fmt.Fprintf(w, "\npronto. Próximos passos:\n  sentinela%s scan --all   (modo sombra: grava os achados, sem publicar no Linear)\n  sentinela%s report --run <id>   (revise o que sairia como issue)\n  ponha publish.enabled: true no %s e rode sentinela%s scan --all de novo   (publica os pendentes no time %s, como você)\n  depois: sentinela%s serve (lê as decisões a cada %s) e sentinela%s worker (roda os pedidos de correção com %s)\n",
				flag, flag, path, flag, cfg.Defaults.Linear.Team, flag, cfg.Adapters.MCP.PollInterval, flag, cfg.Labels.Trigger)
			return nil
		}
		fmt.Fprintf(w, "\npronto. Próximos passos:\n  sentinela%s scan --all   (as issues saem no time %s do Linear, como você)\n  decida no Linear: cancele com o motivo ou aplique %s\n  sentinela%s serve   (lê as decisões a cada %s)\n  sentinela%s worker  (roda os pedidos de correção)\n",
			flag, cfg.Defaults.Linear.Team, cfg.Labels.Trigger, flag, cfg.Adapters.MCP.PollInterval, flag)
		return nil
	}
	fmt.Fprintf(w, "\npronto. Próximos passos:\n  sentinela%s scan --all\n  abra %s/index.md e decida com `sentinela%s finding ...`\n  sentinela%s worker --once   (roda os pedidos de correção)\n", flag, reportDir, flag, flag)
	return nil
}

// writeEnvIfMissing cria o .env com o DATABASE_URL local quando ele não existe e a variável não
// veio do ambiente. Um .env existente nunca é mexido.
func writeEnvIfMissing(path, url, port, project string, needed bool) error {
	if !needed {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	body := "# Gerado por `sentinela init`. Segredos não entram no git.\n" +
		"# Projeto e porta do docker compose desta instalação: dois clones na mesma máquina não dividem o Postgres.\n" +
		"COMPOSE_PROJECT_NAME=" + project + "\nSENTINELA_PG_PORT=" + port + "\nDATABASE_URL=" + url + "\n"
	return os.WriteFile(path, []byte(body), 0o600)
}

// redactURL tira a senha da URL do banco para mostrar na tela.
func redactURL(u string) string {
	at := strings.Index(u, "@")
	scheme := strings.Index(u, "://")
	if at < 0 || scheme < 0 {
		return u
	}
	creds := u[scheme+3 : at]
	if user, _, ok := strings.Cut(creds, ":"); ok {
		return u[:scheme+3] + user + ":***" + u[at:]
	}
	return u
}

// mcpConnected diz se `claude mcp list` mostra o servidor conectado.
func mcpConnected(list, server string) bool {
	for _, l := range strings.Split(list, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), server+":") && strings.Contains(l, "Connected") && !strings.Contains(l, "Failed") {
			return true
		}
	}
	return false
}

// detectLanguages lê as linguagens de cada repo: do clone do scan (.sentinela/repos), se já existe,
// ou do GitHub. Repo sem resposta fica de fora do mapa e deixa a lista de scanners completa.
func detectLanguages(ctx context.Context, h initHooks, w io.Writer, repos []string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	workdir, _ := filepath.Abs(filepath.Join(".sentinela", "repos"))
	for _, r := range repos {
		if langs := cloneLanguages(workdir, r); langs != nil {
			out[r] = langs
			fmt.Fprintf(w, "ok: linguagens de %s: %s (clone do scan)\n", r, orDash(scan.LanguageList(scannerLangs(langs))))
			continue
		}
		if h.Languages == nil {
			continue
		}
		langs, err := h.Languages(ctx, r)
		if err != nil {
			fmt.Fprintf(w, "aviso: linguagens de %s não detectadas (%v): discovery.scanners sai com a lista completa\n", r, err)
			continue
		}
		out[r] = langs
		fmt.Fprintf(w, "ok: linguagens de %s: %s (GitHub)\n", r, orDash(scan.LanguageList(scannerLangs(langs))))
	}
	return out
}

// scannerLangs fica só com as linguagens que decidem scanner (sem config, html...).
func scannerLangs(langs map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, l := range scan.ScannerLanguages {
		if langs[l] {
			out[l] = true
		}
	}
	return out
}

// MCPConfig é o sentinela.yaml mínimo do modo MCP: o do modo local com o Linear (e o Slack, se
// houver canal) pelos MCPs do Claude Code do usuário (ADR 0039).
func MCPConfig(repos []string, linearTeam, slackChannel string) string {
	p, _ := config.Profile(config.DefaultProfile)
	return MCPConfigWith(repos, linearTeam, slackChannel, p)
}

// MCPConfigWith é o MCPConfig com o perfil de modelos dado.
func MCPConfigWith(repos []string, linearTeam, slackChannel string, p config.ModelProfile) string {
	return mcpConfig(repos, linearTeam, slackChannel, p, planScanners(repos, nil))
}

func mcpConfig(repos []string, linearTeam, slackChannel string, p config.ModelProfile, plan scannerPlan) string {
	notifier := "none          # slack_mcp com --slack-channel"
	if slackChannel != "" {
		notifier = "slack_mcp     # avisos pelo MCP do Slack, como você"
	}
	y := localConfig(repos, p, plan)
	y = strings.Replace(y, "# sentinela.yaml do modo local (gerado por sentinela init --mode local; ADR 0038).",
		"# sentinela.yaml do modo MCP (gerado por sentinela init --mode mcp; ADR 0039).", 1)
	y = strings.Replace(y, "  tracker: local          # linear_api para issues no Linear\n  notifier: none          # slack_api para avisos no Slack\n",
		"  tracker: linear_mcp     # issues pelo MCP do Linear do Claude Code (como você); linear_api para o app OAuth\n  notifier: "+notifier+"\n", 1)
	y = strings.Replace(y, "  local_dir: reports/local\n", "  mcp:\n    linear_server: claude.ai Linear\n    slack_server: claude.ai Slack\n    poll_interval: 15m      # cada leitura do Linear é uma chamada claude -p\n    # model: claude-haiku-5-5   # padrão: o mais barato de pricing.models\n", 1)
	defaults := "defaults:\n  linear:\n    team: " + linearTeam + "\n    labels: [sentinela]\n"
	if slackChannel != "" {
		defaults += "  notify:\n    slack: \"" + slackChannel + "\"\n"
	}
	y = strings.Replace(y, "defaults:\n", defaults, 1)
	return strings.Replace(y, "\nrepos:\n", "\npublish:\n  enabled: false          # modo sombra: grava os achados sem publicar; revise com sentinela report e troque para true\n  max_issues_per_team_per_run: 3   # com enabled: true, até 3 issues por time por rodada\n\nrepos:\n", 1)
}

// LocalConfig é o sentinela.yaml mínimo do modo local com o perfil de modelos padrão.
func LocalConfig(repos []string) string {
	p, _ := config.Profile(config.DefaultProfile)
	return LocalConfigWith(repos, p)
}

// LocalConfigWith é o sentinela.yaml mínimo do modo local: tracker local, notifier none, git_host
// gh_cli, o bloco models completo do perfil, os preços e os repos dados.
func LocalConfigWith(repos []string, p config.ModelProfile) string {
	return localConfig(repos, p, planScanners(repos, nil))
}

// localConfig é o LocalConfigWith com a lista discovery.scanners do plano (pelas linguagens).
func localConfig(repos []string, p config.ModelProfile, plan scannerPlan) string {
	var b strings.Builder
	b.WriteString(`# sentinela.yaml do modo local (gerado por sentinela init --mode local; ADR 0038).
# Sem Linear, Slack nem GitHub Apps: achados em reports/local, decisões por ` + "`sentinela finding`" + `,
# PRs em draft pela conta do gh. Referência completa: docs/configuracao.md e docs/modos.md.

adapters:
  tracker: local          # linear_api para issues no Linear
  notifier: none          # slack_api para avisos no Slack
  git_host: gh_cli        # github_apps para as duas GitHub Apps
  local_dir: reports/local

defaults:
  owners_source: both
  policy:
    auto_approve: false

labels:
  finding: sentinela
  trigger: sentinela-fix
  done: sentinela-done
  failed: sentinela-failed

fix_trigger:              # quem pode pedir a correção com a label de gatilho (ADR 0042)
  allowed: team_members   # só membros do time do Linear dono do achado (no modo local, não se aplica)

budget:
  per_run_usd: ` + strconv.FormatFloat(p.BudgetPerRunUSD, 'f', 2, 64) + `      # só vale com llm.provider anthropic_api
  per_pr_usd: 3.00

` + p.ModelsYAML() + `
llm:
  provider: claude_code   # usa a assinatura do Claude Code (claude -p)
  claude_code:
    concurrency: ` + strconv.Itoa(p.ClaudeCodeConcurrency) + `          # chamadas claude -p em paralelo (perfil ` + p.Name + `)
    timeout: ` + p.ClaudeCodeTimeout.String() + `

pricing:
  batch_discount: 0.5
  cache_write_5m_multiplier: 1.25
  cache_write_1h_multiplier: 2.0
  models:
    claude-haiku-5-5: { input: 0.10, output: 0.50, cache_read: 0.01 }
    claude-sonnet-5-5: { input: 2.00, output: 10.00, cache_read: 0.20 }
    claude-opus-5-5: { input: 4.00, output: 20.00, cache_read: 0.20 }

blocked_paths:
  - "**/billing/**"
  - "**/payment*/**"
  - "**/migrations/**"
  - "**/migration/**"
  - "**/auth/**"
  - ".github/workflows/**"

teams:
  dono:                   # um time só; troque e acrescente times quando o CODEOWNERS pedir
    policy: { auto_approve: false }

discovery:
` + plan.yaml() + `  max_new_repos_per_run: 1
  judge_votes: ` + strconv.Itoa(p.JudgeVotes) + `           # > 1: veredito perto do limiar vota de novo (perfil ` + p.Name + `)
  triage_context_tokens: ` + strconv.Itoa(p.TriageContextTokens) + `
  analysis_context_tokens: ` + strconv.Itoa(p.AnalysisContextTokens) + `

execution:
  concurrency: 1

repos:
`)
	if len(repos) == 0 {
		b.WriteString("  # - { name: owner/repo, team: dono }\n  []\n")
	}
	for _, r := range repos {
		fmt.Fprintf(&b, "  - { name: %s, team: dono }\n", r)
	}
	return b.String()
}

// claudeAuthStatus lê `claude auth status --json` com a lista fechada de ambiente das sessões
// (llm.ClosedEnv): sem ANTHROPIC_*, o que ele mostra é o login que a descoberta e o job vão usar
// (D-18 da revisão de prontidão). Sem login, o comando sai com erro e loggedIn false.
func claudeAuthStatus(ctx context.Context) (bool, string, error) {
	cmd := exec.CommandContext(ctx, "claude", "auth", "status", "--json")
	cmd.Env = llm.ClosedEnv(os.Environ())
	out, runErr := cmd.Output()
	var st struct {
		LoggedIn   bool   `json:"loggedIn"`
		AuthMethod string `json:"authMethod"`
	}
	if err := json.Unmarshal(out, &st); err != nil {
		if runErr != nil {
			return false, "", runErr
		}
		return false, "", fmt.Errorf("saída inesperada: %w", err)
	}
	return st.LoggedIn, st.AuthMethod, nil
}
