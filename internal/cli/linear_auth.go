package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/mcpcall"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
)

// linearMode resolve linear_auth.mode: vazio vira app quando há LINEAR_OAUTH_CLIENT_ID.
func (d *deps) linearMode(cfg *config.Config) string {
	if cfg != nil && cfg.LinearAuth.Mode != "" {
		return cfg.LinearAuth.Mode
	}
	if d.env("LINEAR_OAUTH_CLIENT_ID") != "" {
		return config.LinearAuthApp
	}
	return config.LinearAuthAPIKey
}

// linearApp monta o app OAuth do Sentinela a partir da config e das variáveis de ambiente.
func (d *deps) linearApp(cfg *config.Config) *linear.AppAuth {
	la := cfg.LinearAuth
	return &linear.AppAuth{
		ClientID: d.env("LINEAR_OAUTH_CLIENT_ID"), ClientSecret: d.env("LINEAR_OAUTH_CLIENT_SECRET"),
		Grant: la.Grant, Scopes: la.Scopes, RedirectURI: la.RedirectURI, TokenFile: la.TokenFile,
		TokenURL: d.env("LINEAR_OAUTH_TOKEN_URL"),
	}
}

// linearClient escolhe a identidade (ADR 0032). Com o app, confere o token antes; se ele faltar ou
// não renovar e linear_auth.fallback_api_key estiver ligado, usa a chave pessoal e devolve o aviso.
func (d *deps) linearClient(cfg *config.Config) (*linear.Client, string) {
	endpoint, key := d.env("LINEAR_API_URL"), d.env("LINEAR_API_KEY")
	if cfg == nil || d.linearMode(cfg) == config.LinearAuthAPIKey {
		return linear.NewClient(endpoint, key), ""
	}
	app := d.linearApp(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := app.Header(ctx); err != nil {
		if cfg.LinearAuth.FallbackAPIKey && key != "" {
			return linear.NewClient(endpoint, key), fmt.Sprintf("app do Linear indisponível (%v); usando LINEAR_API_KEY (linear_auth.fallback_api_key): as ações saem com o nome da pessoa dona da chave", err)
		}
		return linear.NewClientAuth(endpoint, app, cfg.LinearAuth.LegacyPrefixUntil), fmt.Sprintf("app do Linear indisponível: %v", err)
	}
	legacy := cfg.LinearAuth.LegacyPrefixUntil
	if legacy.IsZero() {
		legacy = app.AuthorizedAt()
	}
	return linear.NewClientAuth(endpoint, app, legacy), ""
}

func (d *deps) linear(cfg *config.Config) *linear.Client {
	c, note := d.linearClient(cfg)
	if note != "" {
		fmt.Fprintf(os.Stderr, "aviso: %s\n", note)
	}
	return c
}

// mcpRunner é o `claude -p` com só um servidor MCP permitido (ADR 0039), gravando em llm_calls
// quando há banco.
func (d *deps) mcpRunner(cfg *config.Config, st *store.Store) *mcpcall.CLI {
	r := &mcpcall.CLI{Binary: cfg.LLM.ClaudeCode.Binary, Model: cfg.Adapters.MCP.Model, WorkDir: cfg.LLM.ClaudeCode.WorkDir, Timeout: cfg.LLM.ClaudeCode.Timeout}
	if st != nil {
		r.Recorder = store.NewLLMRecorder(st)
	}
	_ = os.MkdirAll(r.WorkDir, 0o750)
	return r
}

// tracker é o Linear do modo configurado: a API (app OAuth ou chave) ou o MCP do usuário.
func (d *deps) tracker(cfg *config.Config, st *store.Store) linear.Tracker {
	if cfg.LinearMCP() {
		if d.mcp != nil {
			return d.mcp
		}
		return &linear.MCPTracker{Run: d.mcpRunner(cfg, st), Server: cfg.Adapters.MCP.LinearServer, PollInterval: cfg.Adapters.MCP.PollInterval,
			Teams: cfg.LinearTeamKeys(), FindingLabel: cfg.Labels.Finding}
	}
	return d.linear(cfg)
}

func newLinearCmd(d *deps) *cobra.Command {
	cmd := &cobra.Command{Use: "linear", Short: "Identidade do Sentinela no Linear (app OAuth, ADR 0032)"}
	var timeout time.Duration
	auth := &cobra.Command{
		Use:   "auth",
		Short: "Autoriza o app do Sentinela no Linear (actor=app) e grava o token fora do git",
		Long: "Com linear_auth.grant authorization_code, sobe um servidor temporário no redirect_uri,\n" +
			"mostra o link de autorização (actor=app) e grava o token, com refresh token, em\n" +
			"linear_auth.token_file (0600). Com client_credentials, pede o token direto.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := d.config()
			if err != nil {
				return err
			}
			app := d.linearApp(cfg)
			out := c.OutOrStdout()
			ctx, cancel := context.WithTimeout(c.Context(), timeout)
			defer cancel()
			if cfg.LinearAuth.Grant == config.GrantClientCreds {
				err = app.Fetch(ctx)
			} else {
				err = app.Authorize(ctx, func(link string) {
					fmt.Fprintf(out, "Abra este link e autorize o app Sentinela (esperando o callback em %s):\n\n%s\n\n", app.RedirectURI, link)
				})
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "token gravado em %s (0600)\n", app.TokenFile)
			v, err := linear.NewClientAuth(d.env("LINEAR_API_URL"), app, time.Time{}).Viewer(c.Context())
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Linear: autenticado como %s (app: %v)\n", v.Name, v.App)
			if !v.App {
				return errors.New("o token não é de app: confira se o link tinha actor=app")
			}
			return nil
		},
	}
	auth.Flags().DurationVar(&timeout, "timeout", 10*time.Minute, "quanto esperar pela autorização no navegador")

	whoami := &cobra.Command{
		Use: "whoami", Short: "Mostra com que identidade o Sentinela fala com o Linear", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := d.config()
			if err != nil {
				return err
			}
			lin, note := d.linearClient(cfg)
			if note != "" {
				fmt.Fprintf(c.ErrOrStderr(), "aviso: %s\n", note)
			}
			v, err := lin.Viewer(c.Context())
			if err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "%s (app: %v, modo %s)\n", v.Name, v.App, d.linearMode(cfg))
			return nil
		},
	}

	test := &cobra.Command{
		Use:   "test-comment <issue>",
		Short: "Comenta numa issue para conferir a autoria (como o app; no linear_mcp, como você, com o prefixo)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			cfg, err := d.config()
			if err != nil {
				return err
			}
			var lin linear.Tracker
			if cfg.LinearMCP() {
				lin = d.tracker(cfg, nil)
			} else {
				cl, note := d.linearClient(cfg)
				if note != "" {
					return fmt.Errorf("sem o app: %s", note)
				}
				lin = cl
			}
			ctx := c.Context()
			det, err := lin.Detail(ctx, args[0])
			if err != nil {
				return err
			}
			body := fmt.Sprintf("Teste de identidade do Sentinela (ADR 0032), %s. Pode apagar.", time.Now().Format("2006-01-02 15:04"))
			if err := lin.Comment(ctx, det.ID, body); err != nil {
				return err
			}
			det, err = lin.Detail(ctx, det.ID)
			if err != nil {
				return err
			}
			for i := len(det.Comments.Nodes) - 1; i >= 0; i-- {
				cm := det.Comments.Nodes[i]
				if !strings.Contains(cm.Body, body) {
					continue
				}
				author := "(integração)"
				if cm.User != nil {
					author = fmt.Sprintf("%s (app: %v)", cm.User.Name, cm.User.App)
				}
				fmt.Fprintf(c.OutOrStdout(), "%s: comentário %s por %s; reconhecido como do Sentinela: %v; humano: %v\n",
					args[0], cm.ID, author, cm.BySentinela, cm.Human())
				return nil
			}
			return errors.New("o comentário não apareceu na leitura da issue")
		},
	}
	members := &cobra.Command{
		Use: "members <time>", Short: "Lista os membros de um time do Linear (quem fix_trigger: team_members deixa disparar a correção)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			cfg, err := d.config()
			if err != nil {
				return err
			}
			ms, err := d.tracker(cfg, nil).TeamMembers(c.Context(), args[0])
			if err != nil {
				return err
			}
			for _, m := range ms {
				fmt.Fprintf(c.OutOrStdout(), "%s <%s>\n", m.Name, m.Email)
			}
			fmt.Fprintf(c.OutOrStdout(), "%d membro(s) no time %s\n", len(ms), args[0])
			return nil
		},
	}
	cmd.AddCommand(auth, whoami, test, members)
	return cmd
}
