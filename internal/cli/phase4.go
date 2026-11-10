package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/executor"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
	"github.com/dev-danilocordeiro/sentinela/internal/telemetry"
)

// worker monta o executor com as duas GitHub Apps. Sem a app bot, nada roda; sem a revisora,
// o job roda e a review fica como aviso.
func (d *deps) worker(cfg *config.Config, st *store.Store, log *slog.Logger) (*executor.Worker, error) {
	workdir, err := filepath.Abs(cfg.Discovery.Workdir)
	if err != nil {
		return nil, err
	}
	legacy, err := useLegacyExecutionDirs(context.Background(), cfg, st.Q.ExecutionsUnderWorktree)
	if err != nil {
		return nil, err
	}
	if legacy && log != nil {
		log.Info("diretórios de execução compartilhados de antes da ADR 0056 (há execuções neles); para usar os desta instalação, defina execution.worktrees, jobs e home", "worktrees", cfg.Execution.Worktrees)
	}
	home := jobHome(cfg)
	w := &executor.Worker{
		Cfg: cfg, Store: st, Notify: d.router(cfg), Log: log,
		Git:    &repo.Git{Root: workdir, Token: d.gitToken(context.Background(), cfg), BaseURL: d.env("SENTINELA_GIT_BASE_URL")},
		Runner: executor.ClaudeRunner{Binary: cfg.Execution.Binary, Home: home, Container: jobContainer(cfg)},
	}
	if home.Dir != "" {
		// As sessões do job (retomada e log) ficam no HOME dele.
		w.ClaudeHome = filepath.Join(home.Dir, ".claude")
	}
	if !cfg.LocalTracker() {
		w.Linear = d.tracker(cfg, st)
	}
	if cfg.Adapters.GitHost == config.GitHostGHCLI {
		// A conta do gh abre o PR e comenta a review do gate (ADR 0038).
		w.Bot, w.Reviewer = d.ghcli(), d.ghcli()
	} else {
		bot, err := d.app("bot")
		if err != nil {
			return nil, fmt.Errorf("app bot: %w", err)
		}
		if bot == nil {
			return nil, errors.New("a linha de execução precisa da app bot: defina GITHUB_BOT_APP_ID, GITHUB_BOT_INSTALLATION_ID e GITHUB_BOT_PRIVATE_KEY_PATH (ou use adapters.git_host gh_cli)")
		}
		w.Bot = bot
		rev, err := d.app("reviewer")
		if err != nil {
			return nil, fmt.Errorf("app revisora: %w", err)
		}
		if rev != nil {
			w.Reviewer = rev
		}
	}
	if mode := cfg.Execution.ReviewNotes; mode == "shadow" || mode == "publish" {
		w.AfterPR = func(ctx context.Context, e db.Execution) error {
			res, err := runNotes(ctx, d, cfg, st, noop.NewTracerProvider().Tracer(telemetry.TracerName), e.ID, mode == "shadow")
			if log != nil && err == nil {
				log.Info("notas de review", "execução", e.ID, "modo", mode, "notas", len(res.Notes), "candidatos", len(res.Decisions), "rodada", res.RunID)
			}
			return err
		}
	}
	return w, nil
}

func newWorkerCmd(d *deps) *cobra.Command {
	var once bool
	var concurrency int
	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Consome a fila de execuções (label sentinela-fix) e acompanha os PRs abertos",
		Long: "A cada execution.poll_interval: enfileira as issues em que um humano aplicou a label\n" +
			"de gatilho, roda até execution.concurrency jobs em paralelo (cada um no seu git\n" +
			"worktree, com o plugin sentinela-factory em modo headless) e acompanha os PRs abertos\n" +
			"(PR Shepherd, gate com CI verde, desfecho). PRs sempre em draft; nada é aprovado nem\n" +
			"mergeado pelo Sentinela enquanto github_apps.reviewer_approves estiver desligado.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx := c.Context()
			out := c.OutOrStdout()
			cfg, err := d.config()
			if err != nil {
				return err
			}
			if concurrency > 0 {
				cfg.Execution.Concurrency = concurrency
			}
			st, err := store.Open(ctx, d.env("DATABASE_URL"))
			if err != nil {
				return err
			}
			defer st.Close()
			if err := claimInstallation(ctx, st); err != nil {
				return err
			}
			log := slog.New(slog.NewTextHandler(c.ErrOrStderr(), nil))
			w, err := d.worker(cfg, st, log)
			if err != nil {
				return err
			}
			live := newLiveConfig(d, cfg, c.ErrOrStderr())
			tick := func() error {
				// Relê a config a cada ciclo (ADR 0041): times, módulos, projetos e canais novos valem
				// sem reiniciar; inválida, fica a última válida.
				if next, changed := live.Reload(ctx); changed {
					// A config relida volta aos diretórios padrão: a escolha dos antigos vale de novo.
					if _, err := useLegacyExecutionDirs(ctx, next, st.Q.ExecutionsUnderWorktree); err != nil {
						return err
					}
					cfg = next
					w.Cfg, w.Notify = cfg, d.router(cfg)
				}
				changed, err := workerTick(ctx, w, cfg.Execution.Concurrency, out)
				if changed && cfg.LocalTracker() {
					// Tracker local (ADR 0038): o relatório mostra o job e o PR novos.
					t := d.localTracker(cfg, st)
					for _, rc := range cfg.Repos {
						if _, werr := t.Write(ctx, rc.Name); werr != nil {
							fmt.Fprintf(out, "aviso: relatório local de %s: %v\n", rc.Name, werr)
						}
					}
				}
				return err
			}
			if once {
				return tick()
			}
			fmt.Fprintf(out, "worker: até %d job(s) em paralelo, ciclo a cada %s\n", cfg.Execution.Concurrency, cfg.Execution.Poll)
			t := time.NewTicker(cfg.Execution.Poll)
			defer t.Stop()
			for {
				if err := tick(); err != nil {
					log.Error("worker", "erro", err)
				}
				select {
				case <-ctx.Done():
					return nil
				case <-t.C:
				}
			}
		},
	}
	cmd.Flags().BoolVar(&once, "once", false, "um ciclo: enfileira, roda os jobs da fila e acompanha os PRs, e sai")
	cmd.Flags().IntVar(&concurrency, "concurrency", 0, "jobs em paralelo (padrão: execution.concurrency)")
	return cmd
}

// workerTick enfileira, drena a fila com até n jobs em paralelo e acompanha os PRs. changed diz
// se algum job rodou ou algum PR mudou.
func workerTick(ctx context.Context, w *executor.Worker, n int, out io.Writer) (changed bool, _ error) {
	queued, err := w.Enqueue(ctx)
	if err != nil {
		return false, fmt.Errorf("gatilho: %w", err)
	}
	var ranAny atomic.Bool
	for _, e := range queued {
		fmt.Fprintf(out, "enfileirado: job %d · %s (%s)\n", e.ID, e.IssueIdentifier, e.Repo)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				ran, err := w.RunNext(ctx)
				if err != nil {
					mu.Lock()
					firstErr = errors.Join(firstErr, err)
					mu.Unlock()
					return
				}
				if !ran {
					return
				}
				ranAny.Store(true)
			}
		}()
	}
	wg.Wait()
	if waiting, err := w.Waiting(ctx); err != nil {
		firstErr = errors.Join(firstErr, err)
	} else {
		for _, e := range waiting {
			fmt.Fprintf(out, "aguardando: job %d · %s (%s): %s\n", e.ID, e.IssueIdentifier, e.Repo, e.WaitReason.String)
		}
	}
	res, err := w.FollowUp(ctx)
	for _, ev := range res.Events {
		fmt.Fprintf(out, "PR: %s\n", ev)
	}
	for _, wmsg := range res.Warnings {
		fmt.Fprintf(out, "aviso: %s\n", wmsg)
	}
	// Depois do acompanhamento, que grava merge e fechamento dos PRs (ADR 0037).
	sw, serr := w.SweepWorktrees(ctx)
	printSweep(out, sw)
	rr, rerr := w.SweepRetention(ctx)
	printRetention(out, rr)
	return ranAny.Load() || len(res.Events) > 0, errors.Join(firstErr, err, serr, rerr)
}

// printRetention mostra o que a retenção removeu (D-12).
func printRetention(out io.Writer, res executor.RetentionResult) {
	if res.JobDirs > 0 {
		fmt.Fprintf(out, "retenção: %d diretório(s) de job removido(s) (%s liberados)\n", res.JobDirs, humanBytes(res.Bytes))
	}
	if res.WebhookPayloads > 0 {
		fmt.Fprintf(out, "retenção: corpo de %d webhook(s) processado(s) esvaziado\n", res.WebhookPayloads)
	}
	for _, wmsg := range res.Warnings {
		fmt.Fprintf(out, "aviso: %s\n", wmsg)
	}
}

func printSweep(out io.Writer, res executor.SweepResult) {
	for _, r := range res.Removed {
		fmt.Fprintf(out, "worktree removido: %s · %s (%s, %s liberados)\n", orDash(r.Issue), r.Path, r.Reason, humanBytes(r.Bytes))
	}
	for _, wmsg := range res.Warnings {
		fmt.Fprintf(out, "aviso: %s\n", wmsg)
	}
}

// humanBytes formata um tamanho em B, KB, MB ou GB (base 1024).
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	v, i := float64(n)/unit, 0
	for v >= unit && i < 2 {
		v /= unit
		i++
	}
	return fmt.Sprintf("%.1f %cB", v, "KMG"[i])
}

func newJobCmd(d *deps) *cobra.Command {
	cmd := &cobra.Command{Use: "job", Short: "Execuções da linha de execução: listar, ver e comandos de humano"}
	withStore := func(fn func(ctx context.Context, cfg *config.Config, st *store.Store, c *cobra.Command, args []string) error) func(*cobra.Command, []string) error {
		return func(c *cobra.Command, args []string) error {
			cfg, err := d.config()
			if err != nil {
				return err
			}
			st, err := store.Open(c.Context(), d.env("DATABASE_URL"))
			if err != nil {
				return err
			}
			defer st.Close()
			return fn(c.Context(), cfg, st, c, args)
		}
	}
	var limit int
	list := &cobra.Command{
		Use: "list", Short: "Lista as execuções mais recentes", Args: cobra.NoArgs,
		RunE: withStore(func(ctx context.Context, _ *config.Config, st *store.Store, c *cobra.Command, _ []string) error {
			rows, err := st.Q.ListExecutions(ctx, int32(limit)) //nolint:gosec // limite pequeno da flag
			if err != nil {
				return err
			}
			for _, e := range rows {
				fmt.Fprintf(c.OutOrStdout(), "%4d  %-8s %-9s %-9s %-10s %-8s %s  %s  %s\n", e.ID, e.IssueIdentifier, e.Kind, e.Status,
					orDash(e.Decision.String), orDash(e.Gate.String), orDash(e.PrUrl.String), e.Repo, orDash(e.WaitReason.String))
			}
			return nil
		}),
	}
	list.Flags().IntVar(&limit, "limit", 20, "quantas")
	show := &cobra.Command{
		Use: "show <id>", Short: "Mostra uma execução com reviews e motivos do gate", Args: cobra.ExactArgs(1),
		RunE: withStore(func(ctx context.Context, _ *config.Config, st *store.Store, c *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return err
			}
			e, err := st.Q.GetExecution(ctx, id)
			if err != nil {
				return fmt.Errorf("job %d: %w", id, err)
			}
			reviews, err := st.Q.ReviewsByExecution(ctx, id)
			if err != nil {
				return err
			}
			printJob(c.OutOrStdout(), e, reviews)
			return nil
		}),
	}
	var by string
	approve := &cobra.Command{
		Use: "approve <id>", Short: "Aprova um job em ESCALATE (decisão humana)", Args: cobra.ExactArgs(1),
		Long: "Registra que um humano assumiu a decisão de um job em ESCALATE e reavalia o Policy Gate.\n" +
			"A revisora publica a review do gate (COMMENT enquanto github_apps.reviewer_approves estiver\n" +
			"desligado). O PR continua em draft e o merge continua com o humano.",
		RunE: withStore(func(ctx context.Context, cfg *config.Config, st *store.Store, c *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return err
			}
			if by == "" {
				if u, err := user.Current(); err == nil {
					by = u.Username
				}
			}
			w, err := d.worker(cfg, st, slog.New(slog.NewTextHandler(c.ErrOrStderr(), nil)))
			if err != nil {
				return err
			}
			e, g, err := w.Approve(ctx, id, by)
			if err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "job %d (%s): ESCALATE aprovado por %s\n", e.ID, e.IssueIdentifier, by)
			if g != nil {
				fmt.Fprintf(c.OutOrStdout(), "gate: %s %s\n", g.Decision, strings.Join(g.Reasons, "; "))
			}
			return nil
		}),
	}
	approve.Flags().StringVar(&by, "by", "", "quem aprova (padrão: usuário do sistema)")
	rework := &cobra.Command{
		Use: "rework <id> <nota>", Short: "Devolve o job ao builder com uma nota (rodadas zeradas)", Args: cobra.ExactArgs(2),
		RunE: withStore(func(ctx context.Context, cfg *config.Config, st *store.Store, c *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return err
			}
			w := &executor.Worker{Cfg: cfg, Store: st}
			e, err := w.Rework(ctx, id, args[1])
			if err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "job %d enfileirado: rework do job %d (%s, branch %s); o worker pega no próximo ciclo\n", e.ID, id, e.IssueIdentifier, e.Branch.String)
			return nil
		}),
	}
	retry := &cobra.Command{
		Use: "retry <id>", Short: "Refaz do zero um job que falhou ou foi bloqueado (decisão humana)", Args: cobra.ExactArgs(1),
		RunE: withStore(func(ctx context.Context, cfg *config.Config, st *store.Store, c *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return err
			}
			w := &executor.Worker{Cfg: cfg, Store: st}
			e, err := w.Retry(ctx, id)
			if err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "job %d enfileirado: nova tentativa do job %d (%s); o worker pega no próximo ciclo\n", e.ID, id, e.IssueIdentifier)
			if prev, err := st.Q.GetExecution(ctx, id); err == nil {
				if d := w.PlanResume(ctx, prev); d.Note != "" {
					fmt.Fprintf(c.OutOrStdout(), "sessão: %s (o worker confere de novo ao começar)\n", d.Note)
				}
			}
			return nil
		}),
	}
	cancel := &cobra.Command{
		Use: "cancel <id>", Short: "Cancela um job ainda na fila", Args: cobra.ExactArgs(1),
		RunE: withStore(func(ctx context.Context, _ *config.Config, st *store.Store, c *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return err
			}
			tag, err := st.Pool.Exec(ctx, "UPDATE executions SET status = 'canceled', updated_at = now() WHERE id = $1 AND status = 'queued'", id)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return fmt.Errorf("job %d não está na fila", id)
			}
			fmt.Fprintf(c.OutOrStdout(), "job %d cancelado\n", id)
			return nil
		}),
	}
	sweep := &cobra.Command{
		Use: "sweep", Short: "Remove agora os worktrees de PR mergeado ou fechado e os de falha além do prazo (o worker faz isso a cada ciclo)", Args: cobra.NoArgs,
		RunE: withStore(func(ctx context.Context, cfg *config.Config, st *store.Store, c *cobra.Command, _ []string) error {
			w, err := d.worker(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				return err
			}
			res, err := w.SweepWorktrees(ctx)
			printSweep(c.OutOrStdout(), res)
			rr, rerr := w.SweepRetention(ctx)
			printRetention(c.OutOrStdout(), rr)
			if rerr != nil {
				return rerr
			}
			if err == nil && len(res.Removed) == 0 {
				fmt.Fprintln(c.OutOrStdout(), "nenhum worktree a remover")
			}
			return err
		}),
	}
	cmd.AddCommand(list, show, approve, rework, retry, cancel, sweep)
	return cmd
}

func printJob(w io.Writer, e db.Execution, reviews []db.Review) {
	fmt.Fprintf(w, "job %d · %s · %s · %s · tentativa %d\n", e.ID, e.IssueIdentifier, e.Repo, e.Kind, e.Attempt)
	fmt.Fprintf(w, "status %s · decisão %s · rodadas %d · turnos %d · custo equivalente $%s\n", e.Status, orDash(e.Decision.String), e.Rounds, e.NumTurns, numeric(e.CostUsd))
	if e.DecisionReason.Valid {
		fmt.Fprintf(w, "motivo: %s\n", e.DecisionReason.String)
	}
	if e.WaitReason.Valid {
		fmt.Fprintf(w, "esperando: %s\n", e.WaitReason.String)
	}
	if e.ReworkNote.Valid {
		fmt.Fprintf(w, "nota de rework: %s\n", e.ReworkNote.String)
	}
	if e.ResumeNote.Valid {
		fmt.Fprintf(w, "sessão: %s\n", e.ResumeNote.String)
	}
	fmt.Fprintf(w, "branch %s · worktree %s · job %s\n", orDash(e.Branch.String), orDash(e.Worktree.String), orDash(e.JobDir.String))
	if e.SessionID.Valid {
		var log executor.SessionLog
		_ = json.Unmarshal(e.SessionLog, &log)
		fmt.Fprintf(w, "sessão %s · %s (%d subagentes)\n", e.SessionID.String, orDash(log.Main), len(log.Subagents))
	}
	if e.PrUrl.Valid {
		fmt.Fprintf(w, "PR %s (%s) · head %s\n", e.PrUrl.String, orDash(e.PrState.String), short(e.HeadSha.String))
	}
	if e.Gate.Valid {
		var reasons []string
		_ = json.Unmarshal(e.GateReasons, &reasons)
		fmt.Fprintf(w, "gate %s: %s\n", e.Gate.String, strings.Join(reasons, "; "))
	}
	if e.ApprovedBy.Valid {
		fmt.Fprintf(w, "ESCALATE aprovado por %s em %s\n", e.ApprovedBy.String, e.ApprovedAt.Time.Format(time.RFC3339))
	}
	if e.ShepherdCycles > 0 {
		fmt.Fprintf(w, "PR Shepherd: %d ciclo(s)\n", e.ShepherdCycles)
	}
	for _, r := range reviews {
		var items []string
		_ = json.Unmarshal(r.Findings, &items)
		fmt.Fprintf(w, "  rodada %d · %-11s %s", r.Round, r.Lens, r.Verdict)
		if len(items) > 0 {
			fmt.Fprintf(w, " · %s", strings.Join(items, " | "))
		}
		fmt.Fprintln(w)
	}
	if e.Error.Valid {
		fmt.Fprintf(w, "erro: %s\n", e.Error.String)
	}
}

func newMetricsCmd(d *deps) *cobra.Command {
	return &cobra.Command{
		Use: "metrics", Short: "Métricas da linha de execução: bloqueios por lente, rodadas e custo por job", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := store.Open(c.Context(), d.env("DATABASE_URL"))
			if err != nil {
				return err
			}
			defer st.Close()
			w := c.OutOrStdout()
			jobs, err := st.Q.JobMetrics(c.Context())
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "jobs %d · approve %d · escalate %d · falhos/bloqueados %d · rodadas médias %.1f · custo equivalente total $%.2f (médio $%.2f)\n",
				jobs.Jobs, jobs.Approved, jobs.Escalated, jobs.Failed, jobs.AvgRounds, jobs.TotalCost, jobs.AvgCost)
			lenses, err := st.Q.LensMetrics(c.Context())
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "%-12s %8s %10s %14s %22s\n", "lente", "reviews", "bloqueios", "jobs barrados", "confirmados (humano)")
			for _, l := range lenses {
				fmt.Fprintf(w, "%-12s %8d %10d %14d %22d\n", l.Lens, l.Reviews, l.Blocks, l.JobsBlocked, l.Confirmed)
			}
			return nil
		},
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func numeric(n pgtype.Numeric) string {
	nano, err := store.NumericToNanoUSD(n)
	if err != nil {
		return "?"
	}
	return strconv.FormatFloat(float64(nano)/1e9, 'f', 2, 64)
}

// jobHome monta o HOME próprio do job (M6) com o login do Claude Code da máquina como origem:
// CLAUDE_CONFIG_DIR, se definido, ou ~/.claude.
func jobHome(cfg *config.Config) executor.JobHome {
	if cfg.Execution.Home == config.HomeOff {
		return executor.JobHome{}
	}
	src := os.Getenv("CLAUDE_CONFIG_DIR")
	if src == "" {
		if h, err := os.UserHomeDir(); err == nil {
			src = filepath.Join(h, ".claude")
		}
	}
	return executor.JobHome{Dir: cfg.Execution.Home, Source: src, Files: cfg.Execution.HomeFiles}
}

// claimInstallation confere, no serve e no worker, se o banco é desta instalação (o diretório
// atual), e registra o diretório num banco que ainda não tem um: são os processos que rodam no
// diretório da instalação (D-06 da revisão de prontidão, ADR 0052).
func claimInstallation(ctx context.Context, st *store.Store) error {
	dir, err := store.HomeDir()
	if err != nil {
		return err
	}
	return store.ClaimHome(ctx, st.Q, dir, true)
}

// jobContainer monta o runtime container do job (ADR 0053); nil no runtime host.
func jobContainer(cfg *config.Config) *executor.ContainerJob {
	e := cfg.Execution
	if e.Runtime != config.RuntimeContainer {
		return nil
	}
	return &executor.ContainerJob{
		Engine: e.Container.Engine, Image: e.Container.Image, Egress: e.Container.EgressAllow,
		Dind: e.JobDocker == config.JobDockerDind, DindImage: e.Container.DindImage, Labels: e.Container.Labels,
	}
}

// useLegacyExecutionDirs mantém nos diretórios compartilhados de antes da ADR 0056 os diretórios
// de execução que vieram do padrão, numa instalação que já tem execuções lá: os jobs em andamento,
// as sessões retomáveis (no HOME do job) e a limpeza dos worktrees continuam achando o que é
// deles. inUse diz se há execução com worktree sob o prefixo.
func useLegacyExecutionDirs(ctx context.Context, cfg *config.Config, inUse func(ctx context.Context, prefix string) (bool, error)) (bool, error) {
	e := &cfg.Execution
	if e.DefaultDirs == 0 {
		return false, nil
	}
	wt, jobs, home := config.LegacyExecutionDirs()
	used, err := inUse(ctx, wt+string(filepath.Separator))
	if err != nil || !used {
		return false, err
	}
	// Só os que vieram do padrão: o que está no arquivo vale como está.
	if e.DefaultDirs&config.DirWorktrees != 0 {
		e.Worktrees = wt
	}
	if e.DefaultDirs&config.DirJobs != 0 {
		e.Jobs = jobs
	}
	if e.DefaultDirs&config.DirHome != 0 {
		e.Home = home
	}
	return true, nil
}
