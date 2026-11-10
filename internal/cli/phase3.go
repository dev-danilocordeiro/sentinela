package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/trace"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/discovery"
	"github.com/dev-danilocordeiro/sentinela/internal/executor"
	"github.com/dev-danilocordeiro/sentinela/internal/feedback"
	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
	"github.com/dev-danilocordeiro/sentinela/internal/notify"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
	"github.com/dev-danilocordeiro/sentinela/internal/telemetry"
)

// router monta o roteador de avisos; sem SLACK_BOT_TOKEN ou com notifier none, devolve nil.
func (d *deps) router(cfg *config.Config) *notify.Router {
	switch cfg.Adapters.Notifier {
	case config.NotifierNone:
		return nil
	case config.NotifierSlackMCP:
		// Pelo MCP do Slack do usuário (ADR 0039); grava em llm_calls quando houver banco.
		var st *store.Store
		if url := d.env("DATABASE_URL"); url != "" {
			st, _ = store.Open(context.Background(), url)
		}
		return &notify.Router{Notifier: &notify.SlackMCP{Run: d.mcpRunner(cfg, st), Server: cfg.Adapters.MCP.SlackServer}, Cfg: cfg}
	}
	sl := d.slack()
	if sl.Token == "" {
		return nil
	}
	return &notify.Router{Notifier: sl, Cfg: cfg}
}

// runSummary é o resumo da rodada para o canal padrão do Slack.
func runSummary(repoName string, res discovery.Result) string {
	s := res.Stats
	var b strings.Builder
	fmt.Fprintf(&b, "*Sentinela · rodada %d · %s* (%s, %s", res.RunID, notify.Escape(repoName), s.Mode, res.Status)
	if res.NoChanges {
		fmt.Fprintf(&b, ": nada mudou desde %s", short(s.HeadSHA))
	} else {
		fmt.Fprintf(&b, ", %d chamadas, %s", res.Calls, res.Elapsed.Round(time.Second))
	}
	b.WriteString(")\n")
	if !res.NoChanges {
		var sigs []string
		for _, r := range s.Scanners {
			sigs = append(sigs, notify.Escape(r.Summary()))
		}
		fmt.Fprintf(&b, "Pacotes aprofundados %d de %d · propostos %d · aprovados %d · duplicados %d · suprimidos %d · descartados %d\n",
			s.Deepened, s.PackagesAnalyzed, s.Proposed, s.Published, s.Duplicates, s.Suppressed, s.Discarded)
		if s.Merged > 0 || s.JudgeSkipped > 0 {
			fmt.Fprintf(&b, "Mesclados em achados abertos %d · pulados pelo Judge (erro ou recusa) %d\n", s.Merged, s.JudgeSkipped)
		}
		if s.InvalidEvidence > 0 || s.EvidenceDropped > 0 || s.Narrowed > 0 {
			fmt.Fprintf(&b, "Evidência inválida: %d achado(s) descartados, %d evidência(s) removidas · escopo reduzido por sobreposição %d\n",
				s.InvalidEvidence, s.EvidenceDropped, s.Narrowed)
		}
		if len(sigs) > 0 {
			fmt.Fprintf(&b, "Scanners: %s\n", strings.Join(sigs, " · "))
		}
		for _, w := range s.Warnings {
			fmt.Fprintf(&b, "Aviso: %s\n", notify.Escape(w))
		}
	}
	if is := s.Issues; is != nil {
		if is.Postponed {
			b.WriteString("Publicação no fim do scan --all, com o limite por time somado entre os repos.\n")
		} else {
			writeIssues(&b, *is)
		}
	}
	if res.PublishErr != nil {
		fmt.Fprintf(&b, "Publicação falhou: %s\n", notify.Escape(res.PublishErr.Error()))
	}
	return strings.TrimSpace(b.String())
}

// writeIssues escreve as issues novas e os adiados pelo limite, por time e severidade.
func writeIssues(b *strings.Builder, is linear.PublishResult) {
	if len(is.Issues) == 0 {
		b.WriteString("Nenhuma issue nova.\n")
	} else {
		fmt.Fprintf(b, "Issues novas (%d):\n", len(is.Issues))
		for _, p := range is.Issues {
			fmt.Fprintf(b, "• %s %s (%s, %s) → %s\n", notify.Link(p.URL, p.Identifier), notify.Escape(p.Title), p.Category, p.Severity, orDefault(p.Team, "padrão"))
			for _, r := range p.Related {
				fmt.Fprintf(b, "   ↳ relacionada %s → %s\n", notify.Link(r.URL, r.Identifier), r.Team)
			}
			if len(p.Overlaps) > 0 {
				fmt.Fprintf(b, "   ↳ escopo reduzido; parte já coberta por %s\n", strings.Join(p.Overlaps, ", "))
			}
		}
	}
	if line := deferredLine(is); line != "" {
		fmt.Fprintf(b, "Adiadas pelo limite por time: %s\n", line)
	}
}

// deferredLine: "plataforma 4 (high 1, low 3) · financeiro 1 (medium 1)", times em ordem alfabética.
func deferredLine(is linear.PublishResult) string {
	teams := make([]string, 0, len(is.Deferred))
	for t := range is.Deferred {
		teams = append(teams, t)
	}
	sort.Strings(teams)
	parts := make([]string, 0, len(teams))
	for _, t := range teams {
		part := fmt.Sprintf("%s %d", t, is.Deferred[t])
		var sev []string
		for _, s := range []string{"critical", "high", "medium", "low"} {
			if n := is.DeferredSeverity[t][s]; n > 0 {
				sev = append(sev, fmt.Sprintf("%s %d", s, n))
			}
		}
		if len(sev) > 0 {
			part += " (" + strings.Join(sev, ", ") + ")"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " · ")
}

// roundSummary é o resumo da publicação do fim do scan --all, para o canal padrão.
func roundSummary(repos []string, is linear.PublishResult, err error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*Sentinela · publicação do scan --all* (%d repo(s), limite por time somado entre os repos)\n", len(repos))
	writeIssues(&b, is)
	for _, w := range is.Warnings {
		fmt.Fprintf(&b, "Aviso: %s\n", notify.Escape(w))
	}
	if err != nil {
		fmt.Fprintf(&b, "Publicação falhou (achados ficam pendentes): %s\n", notify.Escape(err.Error()))
	}
	return strings.TrimSpace(b.String())
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// printPublish mostra o que a rodada publicou. local: tracker local, em que o achado vai para o
// relatório em adapters.local_dir, não para uma issue.
func printPublish(w io.Writer, res discovery.Result, local bool) {
	if is := res.Stats.Issues; is != nil {
		if is.Postponed {
			fmt.Fprintln(w, "publicação no fim do scan --all (limite por time somado entre os repos)")
			return
		}
		printIssues(w, *is, local)
	}
}

func printIssues(w io.Writer, is linear.PublishResult, local bool) {
	kind := "issue"
	if local {
		kind = "achado"
	}
	for _, p := range is.Issues {
		reused := ""
		if p.Reused {
			reused = " (já existia)"
		}
		fmt.Fprintf(w, "%s %s %s%s: %s\n", kind, p.Identifier, p.URL, reused, p.Title)
		for _, r := range p.Related {
			fmt.Fprintf(w, "  relacionada %s %s (%s, %s)\n", r.Identifier, r.URL, r.Team, r.Module)
		}
	}
	if line := deferredLine(is); line != "" {
		fmt.Fprintf(w, "adiadas pelo limite por time: %s\n", line)
	}
	for _, msg := range is.Warnings {
		fmt.Fprintf(w, "aviso: %s\n", msg)
	}
}

func newServeCmd(d *deps) *cobra.Command {
	var once bool
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Sincroniza o estado das issues do Linear (polling ou webhook) e roda o Feedback Curator semanal",
		Long: "Modo polling (trigger.mode): a cada trigger.polling_interval lê o estado das issues\n" +
			"publicadas. Modo webhook: recebe POST /webhooks/linear em trigger.listen_addr, com\n" +
			"assinatura validada (LINEAR_WEBHOOK_SECRET) e dedup por Linear-Delivery. Nos dois modos,\n" +
			"issue cancelada vira achado rejected com supressão gravada, e o Curator roda quando a\n" +
			"última curadoria passou de feedback.curator_interval.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx := c.Context()
			w := c.OutOrStdout()
			cfg, err := d.config()
			if err != nil {
				return err
			}
			st, err := store.Open(ctx, d.env("DATABASE_URL"))
			if err != nil {
				return err
			}
			defer st.Close()
			if err := claimInstallation(ctx, st); err != nil {
				return err
			}
			tp, shutdown, err := telemetry.Setup(ctx, cfg.Telemetry)
			if err != nil {
				return err
			}
			defer func() { _ = shutdown(context.WithoutCancel(ctx)) }()
			tracer := tp.Tracer(telemetry.TracerName)
			syncer := &linear.Syncer{Store: st, Linear: d.tracker(cfg, st), Cfg: cfg}
			router := d.router(cfg)
			if cfg.LocalTracker() {
				// Tracker local (ADR 0038): as decisões chegam pela CLI, direto no Postgres; só a
				// curadoria semanal fica aqui.
				if once {
					return curateIfDue(ctx, d, cfg, st, tracer, w)
				}
				fmt.Fprintln(w, "tracker local: sem sync; curadoria a cada hora, quando vencida")
				live := newLiveConfig(d, cfg, c.ErrOrStderr())
				hourly(ctx, slog.New(slog.NewTextHandler(c.ErrOrStderr(), nil)), func() error {
					cfg, _ = live.Reload(ctx)
					return curateIfDue(ctx, d, cfg, st, tracer, w)
				})
				return nil
			}
			live := newLiveConfig(d, cfg, c.ErrOrStderr())
			tick := func() error {
				// Relê a config a cada ciclo (ADR 0041); inválida, fica a última válida.
				if next, changed := live.Reload(ctx); changed {
					cfg = next
					syncer.Cfg, router = cfg, d.router(cfg)
				}
				res, err := syncer.Poll(ctx)
				if err != nil {
					return err
				}
				printSync(w, res)
				notifyChanges(ctx, router, res.Changes, w)
				resumePublications(ctx, d, cfg, st, tracer, w)
				return curateIfDue(ctx, d, cfg, st, tracer, w)
			}
			if once {
				return tick()
			}
			log := slog.New(slog.NewTextHandler(c.ErrOrStderr(), nil))
			if cfg.Trigger.Mode == config.TriggerWebhook {
				secret := d.env("LINEAR_WEBHOOK_SECRET")
				if secret == "" {
					return linear.ErrNoSecret
				}
				mux := http.NewServeMux()
				mux.Handle("POST /webhooks/linear", &linear.WebhookHandler{Secret: secret, Syncer: syncer, Log: log})
				// GitHub (Fase 4): só com GITHUB_WEBHOOK_SECRET e a app bot; senão o worker acompanha
				// os PRs por polling.
				if ghSecret := d.env("GITHUB_WEBHOOK_SECRET"); ghSecret != "" {
					wk, err := d.worker(cfg, st, log)
					if err != nil {
						return fmt.Errorf("webhook do GitHub: %w", err)
					}
					mux.Handle("POST /webhooks/github", &executor.GitHubWebhook{Secret: ghSecret, Store: st, OnEvent: func(ctx context.Context, _ string) error {
						_, err := wk.FollowUp(ctx)
						return err
					}})
					fmt.Fprintf(w, "webhook do GitHub em http://%s/webhooks/github\n", cfg.Trigger.ListenAddr)
				}
				srv := &http.Server{Addr: cfg.Trigger.ListenAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
				go func() {
					<-ctx.Done()
					_ = srv.Shutdown(context.WithoutCancel(ctx))
				}()
				go hourly(ctx, log, func() error {
					resumePublications(ctx, d, cfg, st, tracer, w)
					return curateIfDue(ctx, d, cfg, st, tracer, w)
				})
				fmt.Fprintf(w, "webhook do Linear em http://%s/webhooks/linear\n", cfg.Trigger.ListenAddr)
				if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
					return err
				}
				return nil
			}
			interval := cfg.Trigger.PollingInterval
			if cfg.LinearMCP() {
				// Cada leitura pelo MCP é uma chamada `claude -p`: intervalo próprio (ADR 0039).
				interval = cfg.Adapters.MCP.PollInterval
			}
			fmt.Fprintf(w, "polling do Linear (%s) a cada %s\n", cfg.Adapters.Tracker, interval)
			t := time.NewTicker(interval)
			defer t.Stop()
			for {
				if err := tick(); err != nil {
					log.Error("polling", "err", err) // erro de um ciclo não derruba o serviço
				}
				select {
				case <-ctx.Done():
					return nil
				case <-t.C:
				}
			}
		},
	}
	cmd.Flags().BoolVar(&once, "once", false, "faz um ciclo de polling (e a curadoria, se vencida) e sai")
	return cmd
}

// resumePublications retoma a publicação dos repos que pararam no limite de uso da assinatura,
// depois do reset (ADR 0055): publica os pendentes sem analisar o repo. Erros viram linhas no log;
// o ciclo segue.
func resumePublications(ctx context.Context, d *deps, cfg *config.Config, st *store.Store, tracer trace.Tracer, w io.Writer) {
	if !cfg.Publish.Enabled || cfg.LocalTracker() {
		return
	}
	runner, err := d.discovery(cfg, st, tracer)
	if err != nil {
		fmt.Fprintf(w, "erro: retomada da publicação: %v\n", err)
		return
	}
	paused, err := runner.PausedPublications(ctx)
	if err != nil {
		fmt.Fprintf(w, "erro: retomada da publicação: %v\n", err)
		return
	}
	for _, p := range paused {
		if p.Until.After(time.Now()) {
			continue
		}
		if _, ok := cfg.Repo(p.Repo); !ok {
			continue
		}
		pub, err := linearPublisher(d, cfg, st)
		if err != nil {
			fmt.Fprintf(w, "erro: retomada da publicação de %s: %v\n", p.Repo, err)
			return
		}
		runner.Publisher = pub
		fmt.Fprintf(w, "publicação de %s: retomando depois do limite de uso (pausa até %s)\n", p.Repo, formatTime(p.Until))
		res, err := runner.ResumePublish(ctx, p.Repo)
		if err != nil {
			fmt.Fprintf(w, "erro: retomada da publicação de %s: %v\n", p.Repo, err)
			continue
		}
		printPublish(w, res, false)
		if res.PublishErr != nil {
			fmt.Fprintf(w, "erro: %v\n", res.PublishErr)
			if errors.Is(res.PublishErr, linear.ErrUsagePaused) {
				return // a cota acabou de novo: os outros repos esperam o próximo reset
			}
		}
	}
}

func hourly(ctx context.Context, log *slog.Logger, fn func() error) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if err := fn(); err != nil {
			log.Error("curadoria", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func printSync(w io.Writer, res linear.SyncResult) {
	fmt.Fprintf(w, "sync: %d issue(s) lidas, %d sem mudança desde o último ciclo, %d mudança(s)\n", res.Checked, res.Duplicates, len(res.Changes))
	for _, ch := range res.Changes {
		line := fmt.Sprintf("  %s → %s", ch.Identifier, ch.State)
		if ch.Outcome != "" {
			line += fmt.Sprintf(" · achado %d %s", ch.FindingID, ch.Outcome)
		}
		if ch.SuppressionID != 0 {
			line += fmt.Sprintf(" · supressão %d", ch.SuppressionID)
		}
		if ch.Actor != "" {
			line += " · por " + ch.Actor
		}
		if ch.Reason != "" {
			line += fmt.Sprintf(" · motivo: %q", ch.Reason)
		}
		fmt.Fprintln(w, line)
	}
}

// notifyChanges avisa no canal padrão os achados recusados ou aceitos.
func notifyChanges(ctx context.Context, router *notify.Router, changes []linear.Change, w io.Writer) {
	var lines []string
	for _, ch := range changes {
		switch ch.Outcome {
		case "rejected":
			lines = append(lines, fmt.Sprintf("• %s cancelada: achado %d recusado e suprimido. Motivo: %s", ch.Identifier, ch.FindingID, notify.Escape(ch.Reason)))
		case "accepted":
			lines = append(lines, fmt.Sprintf("• %s concluída: achado %d aceito", ch.Identifier, ch.FindingID))
		}
	}
	if len(lines) == 0 {
		return
	}
	for _, msg := range router.Summary(ctx, "*Sentinela · feedback do Linear*\n"+strings.Join(lines, "\n")) {
		fmt.Fprintf(w, "aviso: %s\n", msg)
	}
}

// curateIfDue roda o Curator em cada repo com desfechos novos quando a curadoria venceu.
func curateIfDue(ctx context.Context, d *deps, cfg *config.Config, st *store.Store, tracer trace.Tracer, w io.Writer) error {
	cur := &feedback.Curator{Cfg: cfg, Store: st, Tracer: tracer}
	due, since, err := cur.Due(ctx)
	if err != nil || !due {
		return err
	}
	return curate(ctx, d, cfg, st, cur, since, "", w)
}

func curate(ctx context.Context, d *deps, cfg *config.Config, st *store.Store, cur *feedback.Curator, since time.Time, only string, w io.Writer) error {
	repos, err := cur.Repos(ctx, since)
	if err != nil {
		return err
	}
	if only != "" {
		repos = []string{only}
	}
	if len(repos) == 0 {
		fmt.Fprintf(w, "curadoria: nenhum desfecho desde %s\n", since.Format(time.DateTime))
		return nil
	}
	prices := llm.NewPriceTable(cfg.Pricing)
	for _, r := range repos {
		client, err := newCaller(d, cfg, st, prices, cur.Tracer, 0, llm.USD(cfg.Budget.PerRunUSD))
		if err != nil {
			return err
		}
		res, err := cur.Curate(ctx, client, r, since)
		if err != nil {
			return fmt.Errorf("curadoria de %s: %w", r, err)
		}
		printCuration(w, res)
	}
	return nil
}

func printCuration(w io.Writer, c feedback.Curation) {
	if c.Skipped != "" {
		fmt.Fprintf(w, "curadoria %s: %d desfecho(s), pulada: %s\n", c.Repo, c.Outcomes, c.Skipped)
		return
	}
	fmt.Fprintf(w, "curadoria %s: %d desfecho(s) → %d exemplo(s), %d supressão(ões) por padrão, %d limiar(es)\n",
		c.Repo, c.Outcomes, len(c.Examples), len(c.Suppressions), len(c.Thresholds))
	for _, e := range c.Examples {
		fmt.Fprintf(w, "  exemplo achado %d: %s\n", e.FindingID, e.Lesson)
	}
	for _, s := range c.Suppressions {
		fmt.Fprintf(w, "  supressão %s (%s/%s): %s\n", s.Pattern, orDefault(s.Module, "repo"), s.Category, s.Reason)
	}
	for _, t := range c.Thresholds {
		fmt.Fprintf(w, "  limiar %s/%s = %.2f: %s\n", orDefault(t.Module, "repo"), t.Category, t.MinConfidence, t.Reason)
	}
	for _, n := range c.Notes {
		fmt.Fprintf(w, "  nota: %s\n", n)
	}
}

func newCurateCmd(d *deps) *cobra.Command {
	var repoName string
	var window time.Duration
	cmd := &cobra.Command{
		Use:   "curate",
		Short: "Roda o Feedback Curator agora (o serve roda sozinho uma vez por semana)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx := c.Context()
			cfg, err := d.config()
			if err != nil {
				return err
			}
			st, err := store.Open(ctx, d.env("DATABASE_URL"))
			if err != nil {
				return err
			}
			defer st.Close()
			tp, shutdown, err := telemetry.Setup(ctx, cfg.Telemetry)
			if err != nil {
				return err
			}
			defer func() { _ = shutdown(context.WithoutCancel(ctx)) }()
			if window == 0 {
				window = cfg.Feedback.CuratorInterval
			}
			cur := &feedback.Curator{Cfg: cfg, Store: st, Tracer: tp.Tracer(telemetry.TracerName)}
			return curate(ctx, d, cfg, st, cur, time.Now().Add(-window), repoName, c.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&repoName, "repo", "", "só este repo (padrão: todos com desfechos na janela)")
	cmd.Flags().DurationVar(&window, "since", 0, "janela de desfechos (padrão: feedback.curator_interval)")
	return cmd
}
