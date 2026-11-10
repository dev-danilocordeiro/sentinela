package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/trace"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/discovery"
	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/report"
	"github.com/dev-danilocordeiro/sentinela/internal/scan"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
	"github.com/dev-danilocordeiro/sentinela/internal/telemetry"
)

func newScanCmd(d *deps) *cobra.Command {
	var o discovery.Options
	var all bool
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Roda a descoberta num repo, grava os achados e publica os aprovados no Linear",
		Long: "Clona ou atualiza o repo, calcula o diff desde o último head_sha (ou o repo inteiro na\n" +
			"primeira vez), roda os scanners, Triager, Analyst, Architect e Judge, e grava os achados.\n" +
			"Com publish.enabled, cria as issues dos aprovados (limite por time) e avisa no Slack; senão,\n" +
			"modo sombra. Antes de chamar modelos, mostra a estimativa de custo por agente\n" +
			"e para se o valor esperado passar do orçamento.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) (retErr error) {
			ctx := c.Context()
			w := c.OutOrStdout()
			cfg, err := d.config()
			if err != nil {
				return err
			}
			if (o.Repo == "") == !all {
				return errors.New("use --repo R ou --all")
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
			tracer := tp.Tracer(telemetry.TracerName)
			if !all {
				return scanRepo(ctx, d, w, cfg, st, tracer, o, nil)
			}
			// Repos do sentinela.yaml, um por vez (agendamento semanal): os já analisados
			// (incremental) e no máximo discovery.max_new_repos_per_run nunca analisados, na
			// ordem do arquivo (ADR 0035). Uma falha não impede os outros; o limite de uso da
			// assinatura para tudo, porque os próximos falhariam do mesmo jeito.
			done, err := st.Q.ReposWithCompletedRun(ctx)
			if err != nil {
				return err
			}
			repos, later := selectAllRepos(cfg, done)
			if len(later) > 0 {
				fmt.Fprintf(w, "nunca analisados, ficam para as próximas rodadas (max_new_repos_per_run %d): %s\n",
					*cfg.Discovery.MaxNewReposPerRun, strings.Join(later, ", "))
			}
			var failed []string
			live := newLiveConfig(d, cfg, c.ErrOrStderr())
			// O limite por time vale para a rodada inteira (M8): a publicação fica para o fim,
			// inclusive quando o limite de uso para a rodada no meio.
			round := &linear.Round{}
			defer func() {
				if err := flushRound(ctx, d, w, cfg, st, round); err != nil && retErr == nil {
					retErr = err
				}
			}()
			for _, name := range repos {
				// Relê a config antes de cada repo (ADR 0041); inválida, fica a última válida.
				cfg, _ = live.Reload(ctx)
				if _, ok := cfg.Repo(name); !ok {
					fmt.Fprintf(w, "\n== %s: saiu do sentinela.yaml durante a rodada; pulado\n", name)
					continue
				}
				ro := o
				ro.Repo = name
				fmt.Fprintf(w, "\n== %s\n", name)
				err := scanRepo(ctx, d, w, cfg, st, tracer, ro, round)
				if err == nil {
					continue
				}
				fmt.Fprintf(w, "erro: %s: %v\n", name, err)
				failed = append(failed, name)
				if llm.UsageLimited(err.Error()) {
					return fmt.Errorf("limite de uso da assinatura em %s; parou antes dos repos seguintes: %w", name, err)
				}
			}
			if len(failed) > 0 {
				return fmt.Errorf("%d repo(s) com erro: %s", len(failed), strings.Join(failed, ", "))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&o.Repo, "repo", "", "repo owner/nome do sentinela.yaml")
	cmd.Flags().BoolVar(&all, "all", false, "repos já analisados e até discovery.max_new_repos_per_run novos, um por vez")
	cmd.Flags().BoolVar(&o.Full, "full", false, "ignora o último head_sha e analisa o repo inteiro")
	cmd.Flags().Float64Var(&o.BudgetUSD, "budget-usd", 0, "teto da rodada em USD (padrão: budget.per_run_usd)")
	cmd.Flags().BoolVar(&o.EstimateOnly, "estimate-only", false, "só mostra a estimativa de custo, sem chamar modelos")
	return cmd
}

// selectAllRepos escolhe, na ordem do sentinela.yaml, os repos do `scan --all`: todos os que já
// têm rodada completa e os primeiros max_new_repos_per_run nunca analisados. later lista os
// novos que ficaram de fora.
func selectAllRepos(cfg *config.Config, analyzed []string) (run, later []string) {
	seen := make(map[string]bool, len(analyzed))
	for _, r := range analyzed {
		seen[r] = true
	}
	budget := *cfg.Discovery.MaxNewReposPerRun
	for _, r := range cfg.Repos {
		switch {
		case seen[r.Name]:
			run = append(run, r.Name)
		case budget > 0:
			run = append(run, r.Name)
			budget--
		default:
			later = append(later, r.Name)
		}
	}
	return run, later
}

// flushRound publica os pendentes dos repos do scan --all (M8) e manda o resumo ao canal padrão.
// Devolve o erro da publicação, que vira o código de saída do scan (ADR 0055).
func flushRound(ctx context.Context, d *deps, w io.Writer, cfg *config.Config, st *store.Store, round *linear.Round) error {
	if len(round.Repos()) == 0 {
		return nil
	}
	if ctx.Err() != nil {
		fmt.Fprintf(w, "scan --all interrompido: a publicação de %d repo(s) fica para a próxima rodada\n", len(round.Repos()))
		return nil
	}
	pub, err := linearPublisher(d, cfg, st)
	if err != nil {
		fmt.Fprintf(w, "erro: publicação do scan --all: %v\n", err)
		return err
	}
	fmt.Fprintf(w, "\n== publicação do scan --all (%d repo(s), limite por time somado entre os repos)\n", len(round.Repos()))
	res, err := round.Flush(ctx, pub)
	printIssues(w, res, false)
	if err != nil {
		fmt.Fprintf(w, "erro: publicação do scan --all (achados ficam pendentes): %v\n", err)
		if res.PausedUntil != nil {
			// As rodadas já fecharam: a pausa vai para elas, para o serve retomar (ADR 0055).
			for _, id := range round.RunIDs() {
				if perr := st.Q.SetRunPublishPause(context.WithoutCancel(ctx), db.SetRunPublishPauseParams{ID: id, Until: pgtype.Timestamptz{Time: *res.PausedUntil, Valid: true}}); perr != nil {
					fmt.Fprintf(w, "erro: gravando a pausa da publicação na rodada %d: %v\n", id, perr)
				}
			}
			fmt.Fprintf(w, "o serve retoma depois de %s, ou rode `sentinela scan --all` depois do reset\n", formatTime(*res.PausedUntil))
		}
	}
	for _, msg := range pub.Notify.Summary(ctx, roundSummary(round.Repos(), res, err)) {
		fmt.Fprintf(w, "aviso: %s\n", msg)
	}
	return err
}

// linearPublisher monta o publisher do Linear com os avisos no Slack.
func linearPublisher(d *deps, cfg *config.Config, st *store.Store) (*linear.Publisher, error) {
	workdir, err := filepath.Abs(cfg.Discovery.Workdir)
	if err != nil {
		return nil, err
	}
	return &linear.Publisher{Cfg: cfg, Store: st, Linear: d.tracker(cfg, st), Notify: d.router(cfg),
		CommandsDoc: func(name string, scope []string) (string, bool) {
			return repo.LocalCommandsDoc(workdir, name, scope...)
		}}, nil
}

// publishOutcome imprime a falha da publicação da rodada, com os achados do repo que continuam
// sem issue e como retomar, e devolve o erro; sem falha, não imprime nada.
func publishOutcome(w io.Writer, repoName string, res discovery.Result, pending int) error {
	if res.PublishErr == nil {
		return nil
	}
	fmt.Fprintf(w, "erro: %v\n", res.PublishErr)
	fmt.Fprintf(w, "%d achado(s) de %s continuam sem issue (pendentes); ", pending, repoName)
	if is := res.Stats.Issues; is != nil && is.PausedUntil != nil {
		fmt.Fprintf(w, "o serve retoma depois de %s, ou rode `sentinela scan --repo %s` depois do reset\n", formatTime(*is.PausedUntil), repoName)
	} else {
		fmt.Fprintf(w, "a próxima rodada tenta de novo (`sentinela scan --repo %s`)\n", repoName)
	}
	return fmt.Errorf("publicação de %s: %w", repoName, res.PublishErr)
}

// formatTime escreve um horário como os avisos mostram (dia/mês hora:minuto e o fuso, se houver).
func formatTime(t time.Time) string {
	s := t.Format("02/01 15:04")
	if name := t.Location().String(); name != "Local" && name != "" {
		s += " (" + name + ")"
	}
	return s
}

// scanRepo roda uma rodada de descoberta num repo e imprime o resumo. Com round (scan --all), a
// publicação no Linear fica para o fim da rodada inteira.
func scanRepo(ctx context.Context, d *deps, w io.Writer, cfg *config.Config, st *store.Store, tracer trace.Tracer, o discovery.Options, round *linear.Round) (err error) {
	scanners, err := scan.ByName(cfg.Discovery.Scanners, cfg.Discovery.SemgrepConfig, cfg.Discovery.SemgrepLanguages, cfg.Discovery.NodeTools)
	if err != nil {
		return err
	}
	runner, err := d.discovery(cfg, st, tracer)
	if err != nil {
		return err
	}
	runner.Scanners = &scan.Runner{Scanners: scanners, Timeout: cfg.Discovery.ScannerTimeout}
	router := d.router(cfg)
	switch {
	case o.EstimateOnly:
	case cfg.LocalTracker():
		// Tracker local (ADR 0038): o relatório em adapters.local_dir sai sempre; nada fora daqui.
		runner.Publisher = d.localTracker(cfg, st)
	case cfg.Publish.Enabled && round != nil:
		runner.Publisher = round
	case cfg.Publish.Enabled:
		pub, err := linearPublisher(d, cfg, st)
		if err != nil {
			return err
		}
		pub.Notify = router
		runner.Publisher = pub
	}
	if cfg.LLM.Provider == config.ProviderAnthropicAPI {
		fmt.Fprintf(w, "sentinela scan %s · provider %s · orçamento %s\n", o.Repo, cfg.LLM.Provider, llm.USD(budgetOr(o.BudgetUSD, cfg.Budget.PerRunUSD)))
	} else {
		fmt.Fprintf(w, "sentinela scan %s · provider %s · limite %d chamadas\n", o.Repo, cfg.LLM.Provider, cfg.LLM.ClaudeCode.MaxCallsPerRun)
	}
	res, err := runner.Run(ctx, o)
	if res.PublishErr != nil && err == nil {
		// A falha da publicação aparece no terminal, depois das issues, e vira código de saída
		// (ADR 0055).
		pending, perr := st.Q.PendingFindings(context.WithoutCancel(ctx), o.Repo)
		if perr != nil {
			return errors.Join(res.PublishErr, perr)
		}
		defer func() { err = publishOutcome(w, o.Repo, res, len(pending)) }()
	}
	if res.RunID != 0 && !o.EstimateOnly {
		defer func() {
			printPublish(w, res, cfg.LocalTracker())
			for _, msg := range router.Summary(context.WithoutCancel(ctx), runSummary(o.Repo, res)) {
				fmt.Fprintf(w, "aviso: %s\n", msg)
			}
		}()
	}
	if res.NoChanges {
		fmt.Fprintf(w, "nada mudou desde o último head_sha (%s): rodada %d sem chamadas\n", short(res.Stats.HeadSHA), res.RunID)
		return err
	}
	printEstimate(w, res.Stats.Estimate, cfg.LLM.Provider, llm.USD(budgetOr(o.BudgetUSD, cfg.Budget.PerRunUSD)), cfg.LLM.ClaudeCode.MaxCallsPerRun)
	if o.EstimateOnly || errors.Is(err, discovery.ErrEstimateOverBudget) {
		fmt.Fprintf(w, "%d pacote(s) a analisar de %d (modo %s); scanners: %s\n",
			res.Stats.PackagesAnalyzed, res.Stats.PackagesTotal, res.Stats.Mode, scannerSummary(res.Stats.Scanners))
		fmt.Fprintf(w, "unidades de build: %s\n", unitsSummary(res.Stats.BuildUnits))
		return err
	}
	if res.RunID != 0 {
		s := res.Stats
		fmt.Fprintf(w, "rodada %d: %s · %d chamadas · custo %s · tempo %s\n", res.RunID, res.Status, res.Calls, res.Cost, res.Elapsed.Round(time.Second))
		fmt.Fprintf(w, "pacotes %d/%d (modo %s), aprofundados %d, propostos %d, duplicados %d, suprimidos %d, publicados %d, descartados %d, mesclados %d\n",
			s.PackagesAnalyzed, s.PackagesTotal, s.Mode, s.Deepened, s.Proposed, s.Duplicates, s.Suppressed, s.Published, s.Discarded, s.Merged)
		fmt.Fprintf(w, "scanners: %s\n", scannerSummary(s.Scanners))
		fmt.Fprintf(w, "unidades de build: %s\n", unitsSummary(s.BuildUnits))
		if s.InvalidEvidence > 0 || s.EvidenceDropped > 0 || s.Narrowed > 0 {
			fmt.Fprintf(w, "evidência inválida: %d achado(s) descartados, %d evidência(s) removidas · escopo reduzido por sobreposição: %d\n",
				s.InvalidEvidence, s.EvidenceDropped, s.Narrowed)
		}
		for _, wmsg := range s.Warnings {
			fmt.Fprintf(w, "aviso: %s\n", wmsg)
		}
		fmt.Fprintf(w, "relatório: sentinela report --run %d\n", res.RunID)
	}
	return err
}

func newReportCmd(d *deps) *cobra.Command {
	var runID int64
	var output string
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Gera o relatório HTML de uma rodada (achados, custo por agente, decisões do juiz)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx := c.Context()
			st, err := store.Open(ctx, d.env("DATABASE_URL"))
			if err != nil {
				return err
			}
			defer st.Close()
			data, err := report.Load(ctx, st.Q, runID)
			if err != nil {
				return err
			}
			if output == "" {
				output = filepath.Join("reports", "run-"+strconv.FormatInt(runID, 10)+".html")
			}
			if err := os.MkdirAll(filepath.Dir(output), 0o750); err != nil {
				return err
			}
			f, err := os.Create(output) //nolint:gosec // caminho escolhido pelo operador
			if err != nil {
				return err
			}
			if err := report.Write(f, data); err != nil {
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
			w := c.OutOrStdout()
			fmt.Fprintf(w, "rodada %d · %s · %s · %s\n", data.Run.ID, data.Run.Repo, data.Run.Status, costLine(data.Cost.String(), data.Budget.String(), data.Budget == 0))
			tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
			fmt.Fprintln(tw, "agente\tmodelo\tprovider\tchamadas\ttempo (soma)\tentrada\tsaída\tcusto\t")
			for _, a := range data.Agents {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%d\t%d\t%s\t\n", a.Agent, a.Models, a.Providers, a.Calls, a.Latency.Round(time.Second), a.Input, a.Output, a.Cost)
			}
			_ = tw.Flush()
			fmt.Fprintf(w, "achados aprovados: %d · decisões do juiz: %d\n", len(data.Findings), len(data.Judge))
			fmt.Fprintf(w, "relatório: %s\n", output)
			return nil
		},
	}
	cmd.Flags().Int64Var(&runID, "run", 0, "id da rodada")
	cmd.Flags().StringVarP(&output, "output", "o", "", "arquivo HTML (padrão reports/run-<id>.html)")
	_ = cmd.MarkFlagRequired("run")
	return cmd
}

// unitsSummary agrupa as unidades de build por tipo, em ordem: "maven: api, api/domain · npm: web".
func unitsSummary(units []repo.BuildUnit) string {
	if len(units) == 0 {
		return "nenhuma (sem manifesto de build)"
	}
	byKind := map[string][]string{}
	var kinds []string
	for _, u := range units {
		if _, ok := byKind[u.Kind]; !ok {
			kinds = append(kinds, u.Kind)
		}
		byKind[u.Kind] = append(byKind[u.Kind], u.Dir)
	}
	sort.Strings(kinds)
	parts := make([]string, 0, len(kinds))
	for _, k := range kinds {
		parts = append(parts, k+": "+strings.Join(byKind[k], ", "))
	}
	return strings.Join(parts, " · ")
}

// costLine é o custo da rodada no report: com orçamento em dólar (API), "custo X de Y"; sem ele
// (assinatura), o custo é o equivalente em preço de lista.
func costLine(cost, budget string, noBudget bool) string {
	if noBudget {
		return "custo " + cost + " (equivalente em preço de lista; assinatura, sem orçamento em dólar)"
	}
	return "custo " + cost + " de " + budget
}

func printEstimate(w io.Writer, e discovery.Estimate, provider string, budget llm.NanoUSD, maxCalls int) {
	fmt.Fprintln(w, "estimativa por agente:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	if provider != config.ProviderAnthropicAPI {
		fmt.Fprintln(tw, "agente\tmodelo\tchamadas esperadas\tpior caso\t")
		for _, a := range e.Agents {
			fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t\n", a.Agent, a.Model, a.Calls, a.WorstCalls)
		}
		fmt.Fprintf(tw, "total\t\t%d\t%d\t\n", e.ExpectedCalls, e.WorstCalls)
		_ = tw.Flush()
		fmt.Fprintf(w, "provider %s: sem custo por token; limite de %d chamadas (cada nova tentativa por schema inválido também conta)\n", provider, maxCalls)
		if maxCalls > 0 && e.WorstCalls > maxCalls {
			top := topAgent(e, func(a discovery.AgentEstimate) float64 { return float64(a.WorstCalls) })
			fmt.Fprintf(w, "aviso: o pior caso (%d chamadas) passa do teto de %d (llm.claude_code.max_calls_per_run); a maior parte é do %s.\n", e.WorstCalls, maxCalls, top)
			fmt.Fprintf(w, "  se a rodada chegar ao teto, ela para no meio (aborted_budget) e o que faltou fica para a próxima rodada.\n")
			fmt.Fprintf(w, "  %s\n", worstCaseAdvice(top, "aumente llm.claude_code.max_calls_per_run para %d", e.WorstCalls))
		}
		return
	}
	fmt.Fprintln(tw, "agente\tmodelo\tchamadas\tesperado\tpior caso\t")
	for _, a := range e.Agents {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t\n", a.Agent, a.Model, a.Calls, a.Expected, a.Worst)
	}
	fmt.Fprintf(tw, "total\t\t\t%s\t%s\t\n", e.Expected, e.Worst)
	_ = tw.Flush()
	fmt.Fprintf(w, "orçamento %s: o esperado precisa caber; o pior caso é barrado lote a lote pelo orçamento\n", budget)
	if budget > 0 && e.Worst > budget {
		top := topAgent(e, func(a discovery.AgentEstimate) float64 { return float64(a.Worst) })
		fmt.Fprintf(w, "aviso: o pior caso (%s) passa do teto de %s (budget.per_run_usd ou --budget-usd); a maior parte é do %s.\n", e.Worst, budget, top)
		fmt.Fprintf(w, "  se a rodada chegar ao teto, ela para no lote seguinte (aborted_budget) e o que faltou fica para a próxima rodada.\n")
		fmt.Fprintf(w, "  %s\n", worstCaseAdvice(top, "aumente o teto para %s", e.Worst))
	}
}

// topAgent é o agente com o maior valor no pior caso.
func topAgent(e discovery.Estimate, value func(discovery.AgentEstimate) float64) string {
	top, best := "", -1.0
	for _, a := range e.Agents {
		if v := value(a); v > best {
			top, best = a.Agent, v
		}
	}
	return top
}

// worstCaseAdvice diz como fazer o pior caso caber: o ajuste do agente que mais pesa e, sempre,
// subir o teto. raise é o formato do conselho de subir o teto, com o valor do pior caso.
func worstCaseAdvice(top, raise string, worst any) string {
	knob := map[string]string{
		"judge":     "diminua discovery.max_findings_per_package (cada achado é uma chamada do Judge) ou discovery.judge_votes",
		"analyst":   "diminua discovery.max_findings_per_package ou rode por partes (o scan incremental só analisa o que mudou)",
		"triager":   "marque com ignore: true os módulos que não precisam de análise (repos[].modules) ou rode por partes",
		"architect": "rode por partes (o scan incremental só analisa o que mudou)",
	}[top]
	advice := "para caber: " + fmt.Sprintf(raise, worst)
	if knob != "" {
		advice += ", ou " + knob
	}
	return advice + "."
}

// newCaller monta o provider de LLM da rodada conforme llm.provider.
func newCaller(d *deps, cfg *config.Config, st *store.Store, prices *llm.PriceTable, tracer trace.Tracer, runID int64, budget llm.NanoUSD) (llm.Caller, error) {
	rec := store.NewLLMRecorder(st)
	switch cfg.LLM.Provider {
	case config.ProviderAnthropicAPI:
		if d.env("ANTHROPIC_API_KEY") == "" {
			return nil, errors.New("ANTHROPIC_API_KEY não definida")
		}
		opts := []option.RequestOption{option.WithAPIKey(d.env("ANTHROPIC_API_KEY"))}
		if base := d.env("ANTHROPIC_BASE_URL"); base != "" {
			opts = append(opts, option.WithBaseURL(base))
		}
		// Chave não vinculada a um workspace exige o header em toda requisição.
		if ws := d.env("ANTHROPIC_WORKSPACE_ID"); ws != "" {
			opts = append(opts, option.WithHeader("anthropic-workspace-id", ws))
		}
		return llm.NewClient(llm.Options{
			API: anthropic.NewClient(opts...), Prices: prices, Recorder: rec,
			Budget: llm.NewBudget(budget), RunID: runID, PollInterval: cfg.Discovery.BatchPollInterval,
			Tracer: tracer, Sync: !cfg.Models.UseBatch,
		})
	case config.ProviderClaudeCode:
		cc := cfg.LLM.ClaudeCode
		cwd := cc.WorkDir // fora de qualquer projeto: o CLI carrega o CLAUDE.md dos diretórios pais
		if err := os.MkdirAll(cwd, 0o750); err != nil {
			return nil, err
		}
		return llm.NewClaudeCode(llm.ClaudeCodeOptions{
			Binary: cc.Binary, Concurrency: cc.Concurrency, Timeout: cc.Timeout, WorkDir: cwd,
			RecordDir: cc.RecordDir, Recorder: rec, Budget: llm.NewCallBudget(cc.MaxCallsPerRun),
			RunID: runID, Tracer: tracer, Prices: prices,
		})
	case config.ProviderReplay:
		return &llm.Replay{Dir: cfg.LLM.Replay.Dir, Recorder: rec, B: llm.NewCallBudget(cfg.LLM.ClaudeCode.MaxCallsPerRun), RunID: runID}, nil
	}
	return nil, fmt.Errorf("llm.provider desconhecido %q", cfg.LLM.Provider)
}

func scannerSummary(rs []scan.Result) string {
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		parts = append(parts, r.Summary())
	}
	return strings.Join(parts, "; ")
}

func budgetOr(v, def float64) float64 {
	if v > 0 {
		return v
	}
	return def
}

func short(s string) string {
	if len(s) > 10 {
		return s[:10]
	}
	return s
}

// discovery monta o Runner da descoberta (scan e notas de review).
func (d *deps) discovery(cfg *config.Config, st *store.Store, tracer trace.Tracer) (*discovery.Runner, error) {
	workdir, err := filepath.Abs(cfg.Discovery.Workdir)
	if err != nil {
		return nil, err
	}
	prices := llm.NewPriceTable(cfg.Pricing)
	return &discovery.Runner{
		Cfg: cfg, Store: st, Prices: prices, Tracer: tracer, Embedder: discovery.HashEmbedder{},
		Git: &repo.Git{Root: workdir, Token: d.gitToken(context.Background(), cfg), BaseURL: d.env("SENTINELA_GIT_BASE_URL")},
		NewLLM: func(runID int64, budget llm.NanoUSD) (llm.Caller, error) {
			return newCaller(d, cfg, st, prices, tracer, runID, budget)
		},
	}, nil
}
