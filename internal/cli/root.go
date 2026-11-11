// Package cli monta os subcomandos cobra do binário sentinela.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/github"
	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/notify"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
)

// Env lê variáveis de ambiente; os testes injetam um mapa.
type Env func(string) string

// NewRootCmd monta a árvore de comandos. Segredos e URLs vêm só de variáveis de ambiente.
func NewRootCmd(env Env) *cobra.Command { return newRootCmd(env, nil) }

func newRootCmd(env Env, hooks *initHooks) *cobra.Command {
	if env == nil {
		env = os.Getenv
	}
	var cfgPath string
	root := &cobra.Command{
		Use:           "sentinela",
		Short:         "Autonomous Reliability Engineering: acha problemas, abre issues e propõe correções",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&cfgPath, "config", "sentinela.yaml", "caminho do sentinela.yaml")

	deps := &deps{env: env, cfgPath: &cfgPath, hooks: hooks}
	root.AddCommand(newMigrateCmd(deps), newConfigCmd(deps), newOwnersCmd(deps), newScanCmd(deps), newReportCmd(deps), newServeCmd(deps), newCurateCmd(deps), newWorkerCmd(deps), newJobCmd(deps), newMetricsCmd(deps), newNotesCmd(deps), newRejudgeCmd(deps), newLinearCmd(deps), newStatusCmd(deps), newRefreshVulnCmd(deps), newPublishCmd(deps), newFindingCmd(deps), newInitCmd(deps), newDocsCmd(deps), newEgressProxyCmd(), newEvalCmd(deps))
	return root
}

type deps struct {
	env     Env
	cfgPath *string
	gh      *github.GHCLI
	hooks   *initHooks     // testes: pontas falsas do init
	mcp     linear.Tracker // testes: tracker linear_mcp falso
}

// ghcli é o gh autenticado do usuário (adapters.git_host gh_cli, ADR 0038).
func (d *deps) ghcli() *github.GHCLI {
	if d.gh == nil {
		d.gh = &github.GHCLI{Bin: d.env("SENTINELA_GH_BIN"), Base: d.env("GITHUB_API_URL")}
	}
	return d.gh
}

// gitToken é o token do git para clone e fetch: GITHUB_TOKEN; com gh_cli e sem ele, o do gh.
func (d *deps) gitToken(ctx context.Context, cfg *config.Config) string {
	if t := d.env("GITHUB_TOKEN"); t != "" || cfg.Adapters.GitHost != config.GitHostGHCLI {
		return t
	}
	t, _ := d.ghcli().Token(ctx)
	return t
}

func (d *deps) config() (*config.Config, error) { return config.Load(*d.cfgPath) }

func (d *deps) slack() *notify.Slack {
	return &notify.Slack{Token: d.env("SLACK_BOT_TOKEN"), BaseURL: d.env("SLACK_API_URL")}
}

func (d *deps) github() *github.Client {
	return github.NewClient(d.env("GITHUB_API_URL"), d.env("GITHUB_TOKEN"))
}

// app carrega uma GitHub App das variáveis GITHUB_<ROLE>_APP_ID, _INSTALLATION_ID e
// _PRIVATE_KEY_PATH. Devolve nil, nil se nenhuma delas estiver definida.
func (d *deps) app(role string) (*github.App, error) {
	prefix := "GITHUB_" + strings.ToUpper(role) + "_"
	id, inst, key := d.env(prefix+"APP_ID"), d.env(prefix+"INSTALLATION_ID"), d.env(prefix+"PRIVATE_KEY_PATH")
	if id == "" && inst == "" && key == "" {
		return nil, nil
	}
	if id == "" || inst == "" || key == "" {
		return nil, fmt.Errorf("defina %sAPP_ID, %sINSTALLATION_ID e %sPRIVATE_KEY_PATH", prefix, prefix, prefix)
	}
	base := d.env("GITHUB_APPS_API_URL") // só para testes; vazio usa GITHUB_API_URL
	if base == "" {
		base = d.env("GITHUB_API_URL")
	}
	return github.LoadApp(id, inst, key, base)
}

// appChecks monta as duas apps para o validate; app não configurada vira erro só se a config
// nomeia a app (github_apps.bot/reviewer).
func (d *deps) appChecks() []AppCheck {
	cfg, _ := d.config()
	var out []AppCheck
	for _, role := range []string{"bot", "reviewer"} {
		a, err := d.app(role)
		named := cfg != nil && ((role == "bot" && cfg.GitHubApps.Bot != "") || (role == "reviewer" && cfg.GitHubApps.Reviewer != ""))
		switch {
		case err != nil:
			out = append(out, AppCheck{Role: role, Err: err})
		case a != nil:
			out = append(out, AppCheck{Role: role, App: a})
		case named:
			out = append(out, AppCheck{Role: role, Missing: true})
		}
	}
	return out
}

func newMigrateCmd(d *deps) *cobra.Command {
	cmd := &cobra.Command{Use: "migrate", Short: "Aplica ou desfaz migrations do Postgres (DATABASE_URL)"}
	withMigrator := func(fn func(context.Context, *store.Migrator, io.Writer) error) func(*cobra.Command, []string) error {
		return func(c *cobra.Command, _ []string) error {
			m, err := store.NewMigrator(d.env("DATABASE_URL"))
			if err != nil {
				return err
			}
			defer func() { _ = m.Close() }()
			return fn(c.Context(), m, c.OutOrStdout())
		}
	}
	up := &cobra.Command{
		Use: "up", Short: "Aplica todas as migrations pendentes", Args: cobra.NoArgs,
		RunE: withMigrator(func(ctx context.Context, m *store.Migrator, w io.Writer) error {
			res, err := m.Up(ctx)
			for _, r := range res {
				fmt.Fprintf(w, "up   %s (%s)\n", r.Source.Path, r.Duration)
			}
			if err == nil && len(res) == 0 {
				fmt.Fprintln(w, "nada a aplicar")
			}
			return err
		}),
	}
	var all bool
	down := &cobra.Command{
		Use: "down", Short: "Desfaz a última migration (--all desfaz todas)", Args: cobra.NoArgs,
		RunE: withMigrator(func(ctx context.Context, m *store.Migrator, w io.Writer) error {
			if all {
				res, err := m.DownTo(ctx, 0)
				for _, r := range res {
					fmt.Fprintf(w, "down %s (%s)\n", r.Source.Path, r.Duration)
				}
				return err
			}
			r, err := m.Down(ctx)
			if r != nil {
				fmt.Fprintf(w, "down %s (%s)\n", r.Source.Path, r.Duration)
			}
			return err
		}),
	}
	down.Flags().BoolVar(&all, "all", false, "desfaz todas as migrations")
	status := &cobra.Command{
		Use: "status", Short: "Mostra o estado das migrations", Args: cobra.NoArgs,
		RunE: withMigrator(func(ctx context.Context, m *store.Migrator, w io.Writer) error {
			st, err := m.Status(ctx)
			for _, s := range st {
				fmt.Fprintf(w, "%-8s %s\n", s.State, s.Source.Path)
			}
			return err
		}),
	}
	cmd.AddCommand(up, down, status)
	return cmd
}

// codeownersFlags lê --codeowners owner/repo=arquivo.
func codeownersFlags(specs []string) (repo.CodeownersSource, error) {
	out := repo.StaticCodeowners{}
	for _, sp := range specs {
		name, file, ok := strings.Cut(sp, "=")
		if !ok {
			return nil, fmt.Errorf("--codeowners %q: use owner/repo=caminho", sp)
		}
		f, err := os.Open(file) //nolint:gosec // arquivo indicado pelo operador
		if err != nil {
			return nil, err
		}
		co, err := repo.ParseCodeowners(f)
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		out[name] = co
	}
	return out, nil
}

// ErrValidation faz `config validate` sair com código diferente de zero.
var ErrValidation = errors.New("config inválida")

func newConfigCmd(d *deps) *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Valida ou gera o sentinela.yaml"}

	var offline bool
	var changed, codeowners []string
	var base string
	validate := &cobra.Command{
		Use:   "validate",
		Short: "Checa a config localmente e contra Linear e GitHub (times, projetos, estados, owners sem time)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			w := c.OutOrStdout()
			cfg, err := d.config()
			if err != nil {
				// Uma linha por erro; as de continuação (recuadas, como as do decoder do YAML) vão
				// junto do erro a que pertencem.
				var lines []string
				for _, l := range strings.Split(err.Error(), "\n") {
					switch {
					case strings.TrimSpace(l) == "":
					case len(lines) > 0 && (strings.HasPrefix(l, " ") || strings.HasSuffix(lines[len(lines)-1], ":")):
						lines[len(lines)-1] += " " + strings.TrimSpace(l)
					default:
						lines = append(lines, l)
					}
				}
				for _, l := range lines {
					fmt.Fprintf(w, "erro: %s\n", l)
				}
				fmt.Fprintf(w, "%d erro(s)\n", len(lines))
				return ErrValidation
			}
			fmt.Fprintf(w, "ok: %s (%d times, %d repos)\n", *d.cfgPath, len(cfg.Teams), len(cfg.Repos))
			for _, team := range cfg.TeamNames() {
				if f := cfg.TeamFile(team); f != "" {
					fmt.Fprintf(w, "ok: time %s em %s\n", team, strings.Trim(f, " ()"))
				}
			}
			if offline {
				// Estrutura e política já passaram no Load; o roteamento é conferido sem chamar ninguém.
				fmt.Fprintln(w, "ok: estrutura, política e roteamento conferidos sem Linear, Slack nem GitHub (--offline)")
				for _, line := range effectiveProjects(cfg) {
					fmt.Fprintf(w, "ok: %s\n", line)
				}
				if len(changed) > 0 {
					co, err := codeownersFlags(codeowners)
					if err != nil {
						return err
					}
					lines, err := changedReport(c.Context(), *d.cfgPath, cfg, changed, base, co)
					if err != nil {
						return err
					}
					for _, l := range lines {
						fmt.Fprintln(w, l)
					}
				}
				warns := append(unownedPathWarnings(cfg), scannerCoverageWarnings(cfg, d.initHooks().LookPath)...)
				for _, msg := range warns {
					fmt.Fprintf(w, "aviso: %s\n", msg)
				}
				if len(warns) > 0 {
					fmt.Fprintf(w, "0 erro(s), %d aviso(s)\n", len(warns))
					return nil
				}
				fmt.Fprintln(w, "0 erro(s)")
				return nil
			}
			var slack SlackAPI
			if sl := d.slack(); sl.Token != "" {
				slack = sl
			}
			gh := d.github()
			if cfg.Adapters.GitHost == config.GitHostGHCLI && d.env("GITHUB_TOKEN") == "" {
				tok, err := d.ghcli().Token(c.Context())
				if err != nil {
					fmt.Fprintf(w, "erro: %v\n", err)
					return ErrValidation
				}
				gh = github.NewClient(d.env("GITHUB_API_URL"), tok)
			}
			var lin LinearAPI
			if !cfg.LocalTracker() {
				lin = d.tracker(cfg, nil)
			}
			rep := ValidateRemote(c.Context(), cfg, lin, gh, slack, d.appChecks()...)
			for _, msg := range append(unownedPathWarnings(cfg), scannerCoverageWarnings(cfg, d.initHooks().LookPath)...) {
				rep.warnf("%s", msg)
			}
			if url := d.env("DATABASE_URL"); url != "" {
				if st, err := store.Open(c.Context(), url); err == nil {
					validateDecisions(c.Context(), st.Q, &rep)
					st.Close()
				} else {
					rep.warnf("banco: %v", err)
				}
			}
			for _, msg := range rep.Info {
				fmt.Fprintf(w, "ok: %s\n", msg)
			}
			for _, msg := range rep.Warnings {
				fmt.Fprintf(w, "aviso: %s\n", msg)
			}
			for _, msg := range rep.Errors {
				fmt.Fprintf(w, "erro: %s\n", msg)
			}
			fmt.Fprintf(w, "%d erro(s), %d aviso(s)\n", len(rep.Errors), len(rep.Warnings))
			if len(rep.Errors) > 0 {
				return ErrValidation
			}
			return nil
		},
	}
	validate.Flags().BoolVar(&offline, "offline", false, "valida estrutura, política e roteamento sem chamar Linear, Slack nem GitHub")
	validate.Flags().StringSliceVar(&changed, "changed", nil, "com --offline: arquivos mudados (ex.: config.d/teams/pagamentos.yaml); mostra o owners explain dos caminhos afetados, antes e depois")
	validate.Flags().StringVar(&base, "base", "HEAD", "com --changed: commit de comparação (no PR, a branch de destino, ex.: origin/main)")
	validate.Flags().StringSliceVar(&codeowners, "codeowners", nil, "com --changed: CODEOWNERS local de um repo, como owner/repo=caminho (repita para vários)")

	var repoName, linearTeam, output, models string
	bootstrap := &cobra.Command{
		Use:   "bootstrap",
		Short: "Gera um sentinela.yaml inicial a partir do CODEOWNERS e dos times do Linear",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			prof, err := config.Profile(models)
			if err != nil {
				return err
			}
			base, err := d.config()
			if err != nil {
				return fmt.Errorf("a config base (orçamento, modelos, preços) vem de --config: %w", err)
			}
			out, notes, err := Bootstrap(c.Context(), base, repoName, linearTeam, d.linear(base), d.github())
			if err != nil {
				return err
			}
			// Personalizado mantém os modelos da config base; os outros perfis os substituem.
			comment := "# Modelos: perfil personalizado, copiado de --config (" + base.ModelsProfile() + "); troque cada papel como quiser (docs/custos.md).\n"
			if models != config.ProfileCustom {
				prof.Apply(out)
				comment = strings.SplitAfter(prof.ModelsYAML(), "models:\n")[0]
				comment = strings.TrimSuffix(comment, "models:\n")
			}
			raw, err := yaml.Marshal(out)
			if err != nil {
				return err
			}
			data := []byte(modelsLine.ReplaceAllLiteralString(string(raw), comment+"models:"))
			var b strings.Builder
			b.WriteString("# Gerado por `sentinela config bootstrap --repo " + repoName + "`. Revise antes de usar.\n")
			for _, n := range notes {
				b.WriteString("# - " + n + "\n")
			}
			b.Write(data)
			if output == "" || output == "-" {
				_, err = io.WriteString(c.OutOrStdout(), b.String())
				return err
			}
			if _, err := os.Stat(output); err == nil {
				return fmt.Errorf("%s já existe; não sobrescrevo", output)
			}
			return os.WriteFile(output, []byte(b.String()), 0o600)
		},
	}
	bootstrap.Flags().StringVar(&repoName, "repo", "", "repo owner/nome")
	bootstrap.Flags().StringVar(&linearTeam, "linear-team", "", "chave do time do Linear (obrigatória se houver mais de um)")
	bootstrap.Flags().StringVarP(&output, "output", "o", "-", "arquivo de saída (- para stdout)")
	bootstrap.Flags().StringVar(&models, "models", config.DefaultProfile, "perfil de modelos do yaml gerado: qualidade, economia ou personalizado (mantém os da config base)")
	_ = bootstrap.MarkFlagRequired("repo")

	var team bool
	schema := &cobra.Command{
		Use: "schema", Short: "Imprime o JSON Schema do sentinela.yaml (ou, com --team, de config.d/teams/<time>.yaml), gerado do código", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			gen := config.Schema
			if team {
				gen = config.TeamSchema
			}
			b, err := gen()
			if err != nil {
				return err
			}
			_, err = c.OutOrStdout().Write(b)
			return err
		},
	}
	schema.Flags().BoolVar(&team, "team", false, "schema do arquivo de um time (config.d/teams/<time>.yaml)")
	cmd.AddCommand(validate, bootstrap, newConfigModelsCmd(d), schema)
	return cmd
}

// modelsLine é a chave models no topo do yaml gerado.
var modelsLine = regexp.MustCompile(`(?m)^models:$`)

func newConfigModelsCmd(d *deps) *cobra.Command {
	var repoName, team string
	cmd := &cobra.Command{
		Use:   "models",
		Short: "Mostra o modelo de cada papel para um repo e um time, de onde ele veio e a que perfil (qualidade, economia) corresponde",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := d.config()
			if err != nil {
				return err
			}
			if _, ok := cfg.Repo(repoName); repoName != "" && !ok {
				return fmt.Errorf("repo %q fora do sentinela.yaml", repoName)
			}
			if cfg.Teams[team] == nil && team != "" {
				return fmt.Errorf("time %q fora do sentinela.yaml", team)
			}
			fmt.Fprintf(c.OutOrStdout(), "bloco models: perfil %s\n", cfg.ModelsProfile())
			tw := tabwriter.NewWriter(c.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "papel\tmodelo\tdefinido em\tperfil")
			for _, m := range cfg.ExplainModels(repoName, team) {
				prof := strings.Join(config.ProfilesOf(m.Role, m.Model), ", ")
				if prof == "" {
					prof = config.ProfileCustom
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", m.Role, m.Model, m.From, prof)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&repoName, "repo", "", "repo (owner/nome) do sentinela.yaml")
	cmd.Flags().StringVar(&team, "team", "", "time dono (chave em teams)")
	return cmd
}

func newOwnersCmd(d *deps) *cobra.Command {
	cmd := &cobra.Command{Use: "owners", Short: "Consulta o roteamento de ownership"}
	var codeownersFile string
	explain := &cobra.Command{
		Use:   "explain <repo> <path> [<path>...]",
		Short: "Mostra qual regra decidiu o dono de um caminho e por quê; com mais caminhos (as outras evidências de um achado), os times secundários",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			cfg, err := d.config()
			if err != nil {
				return err
			}
			var src repo.CodeownersSource
			if codeownersFile != "" {
				f, err := os.Open(codeownersFile) //nolint:gosec // arquivo indicado pelo operador
				if err != nil {
					return err
				}
				co, err := repo.ParseCodeowners(f)
				_ = f.Close()
				if err != nil {
					return err
				}
				src = repo.StaticCodeowners{args[0]: co}
			} else {
				src = d.github().CodeownersSource(c.Context())
			}
			resolver := repo.NewResolver(cfg, src)
			res, err := resolver.Resolve(args[0], args[1])
			if err != nil {
				return err
			}
			printResolution(c.OutOrStdout(), res, cfg.LocalTracker())
			if len(args) > 2 {
				// As outras evidências de um achado: o dono continua o do primeiro caminho; os times
				// que elas tocam entram em "Também afeta" e são mencionados no aviso (ADR 0044).
				resolve := func(p string) (repo.Resolution, error) { return resolver.Resolve(args[0], p) }
				sec := repo.SecondaryTeams(resolve, res.Team, args[2:], nil)
				if len(sec) == 0 {
					fmt.Fprintf(c.OutOrStdout(), "  secundários:     nenhum (os outros caminhos são do time %s ou não têm dono)\n", orDash(res.Team))
				}
				for _, sc := range sec {
					mention := "sem menção"
					if t := cfg.Teams[sc.Team]; t != nil && t.Notify.Mention != "" {
						mention = "menção " + t.Notify.Mention
					}
					fmt.Fprintf(c.OutOrStdout(), "  secundário:      %s (módulo %s: %s; %s no aviso do dono)\n", sc.Team, strings.Join(sc.Modules, ", "), strings.Join(sc.Paths, ", "), mention)
				}
			}
			return nil
		},
	}
	explain.Flags().StringVar(&codeownersFile, "codeowners", "", "usa um CODEOWNERS local em vez de buscar no GitHub")
	cmd.AddCommand(explain, newOwnersPendingCmd(d))
	return cmd
}

// printResolution mostra a resolução de um caminho. No modo local (tracker local), sem Linear nem
// Slack, os campos deles não aparecem.
func printResolution(w io.Writer, res repo.Resolution, local bool) {
	fmt.Fprintf(w, "%s %s\n", res.Repo, res.Path)
	for i, t := range res.Trace {
		fmt.Fprintf(w, "  %d. %s\n", i+1, t)
	}
	if res.Ignored {
		fmt.Fprintf(w, "resultado: ignorado (%s)\n", res.Rule)
		return
	}
	team := res.Team
	if team == "" {
		team = "(nenhum; defaults)"
	}
	fmt.Fprintf(w, "resultado:\n")
	fmt.Fprintf(w, "  time:            %s\n", team)
	fmt.Fprintf(w, "  regra:           %s\n", res.Rule)
	if local {
		if len(res.Subscribers) > 0 {
			fmt.Fprintf(w, "  inscritos:       %s\n", strings.Join(res.Subscribers, ", "))
		}
		fmt.Fprintf(w, "  política:        auto_approve=%s blocked=%s\n", boolStr(res.Policy.AutoApprove), boolStr(res.Policy.Blocked))
		fmt.Fprintf(w, "  (Linear, projeto, assignee e canal não se aplicam no modo local: tracker local)\n")
		for _, wmsg := range res.Warnings {
			fmt.Fprintf(w, "aviso: %s\n", wmsg)
		}
		return
	}
	fmt.Fprintf(w, "  linear:          %s / %q / estado %q\n", res.LinearTeam, res.LinearProject, res.TriageState)
	fmt.Fprintf(w, "  projeto:         %s (de %s)\n", orDash(res.LinearProject), res.ProjectSource)
	for _, cp := range res.CategoryProjects {
		fmt.Fprintf(w, "  por categoria:   %s (teams.%s.linear.category_projects; vale se o projeto não vier de um módulo)\n", cp, res.Team)
	}
	fmt.Fprintf(w, "  assignee:        %s %s\n", res.AssigneeMode, res.Assignee)
	if len(res.Subscribers) > 0 {
		fmt.Fprintf(w, "  inscritos:       %s\n", strings.Join(res.Subscribers, ", "))
	}
	fmt.Fprintf(w, "  canal:           %s (de %s)\n", orDash(res.NotifySlack), res.NotifySource)
	fmt.Fprintf(w, "  menção:          %s (de %s)\n", orDash(res.Mention), res.MentionSource)
	fmt.Fprintf(w, "  política:        auto_approve=%s blocked=%s\n", boolStr(res.Policy.AutoApprove), boolStr(res.Policy.Blocked))
	for _, wmsg := range res.Warnings {
		fmt.Fprintf(w, "aviso: %s\n", wmsg)
	}
}

func boolStr(b *bool) string {
	if b == nil {
		return "-"
	}
	return strconv.FormatBool(*b)
}
