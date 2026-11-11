// Package discovery orquestra uma rodada de descoberta em modo sombra: sincroniza o
// repo, roda as fontes de sinal, deduplica, chama o Judge e grava os achados, sem
// publicar nada fora do Postgres.
package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pgvector/pgvector-go"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/dev-danilocordeiro/sentinela/internal/agents"
	"github.com/dev-danilocordeiro/sentinela/internal/agents/analyst"
	"github.com/dev-danilocordeiro/sentinela/internal/agents/architect"
	"github.com/dev-danilocordeiro/sentinela/internal/agents/judge"
	"github.com/dev-danilocordeiro/sentinela/internal/agents/triager"
	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/feedback"
	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/scan"
	"github.com/dev-danilocordeiro/sentinela/internal/signals"
	"github.com/dev-danilocordeiro/sentinela/internal/signals/code"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
	"github.com/dev-danilocordeiro/sentinela/internal/telemetry"
)

// Runner tem as dependências de uma rodada.
type Runner struct {
	Cfg      *config.Config
	Store    *store.Store
	Git      *repo.Git
	Scanners *scan.Runner
	// NewLLM cria o provider da rodada (anthropic_api, claude_code ou replay) com o run_id.
	// O teto em dólar só vale para anthropic_api; claude_code usa o limite de chamadas da config.
	NewLLM   func(runID int64, budget llm.NanoUSD) (llm.Caller, error)
	Prices   *llm.PriceTable
	Tracer   trace.Tracer
	Embedder Embedder
	// Publisher, se definido, publica no Linear os achados aprovados (e os pendentes de
	// rodadas anteriores) ao fim de cada rodada concluída. Nil = modo sombra.
	Publisher Publisher
}

// Publisher entrega os achados aprovados ao tracker: issues no Linear (linear.Publisher) ou o
// relatório do tracker local (ADR 0038).
type Publisher interface {
	Publish(ctx context.Context, repoName string, runID int64, resolve linear.Resolve) (linear.PublishResult, error)
}

// Options de uma rodada.
type Options struct {
	Repo         string
	Full         bool    // ignora o último head_sha e analisa o repo inteiro
	BudgetUSD    float64 // 0 usa budget.per_run_usd
	EstimateOnly bool    // só mostra a estimativa, sem chamar modelos nem gravar rodada
}

// Stats resume a rodada; vai em analysis_runs.stats e no relatório.
type Stats struct {
	Mode             string        `json:"mode"`
	BaseSHA          string        `json:"base_sha,omitempty"`
	HeadSHA          string        `json:"head_sha"`
	PackagesTotal    int           `json:"packages_total"`
	PackagesAnalyzed int           `json:"packages_analyzed"`
	PackagesIgnored  int           `json:"packages_ignored"`
	Deepened         int           `json:"deepened"`
	ScanSignals      int           `json:"scan_signals"`
	Scanners         []scan.Result `json:"scanners"`
	// BuildUnits são os módulos e pacotes de build do repo (Fase C, passo 3).
	BuildUnits []repo.BuildUnit `json:"build_units,omitempty"`
	Proposed   int              `json:"proposed"`
	Duplicates int              `json:"duplicates"`
	Suppressed int              `json:"suppressed"`
	// InvalidEvidence conta achados descartados antes do Judge por não ter evidência válida;
	// EvidenceDropped, as evidências inválidas removidas de achados que seguiram.
	InvalidEvidence int `json:"invalid_evidence"`
	EvidenceDropped int `json:"evidence_dropped"`
	// Narrowed conta achados publicados com escopo reduzido por sobreposição parcial.
	Narrowed int `json:"narrowed"`
	// LocationCandidates conta achados que foram ao Judge com ao menos um candidato por
	// localização no código (ADR 0025).
	LocationCandidates int      `json:"location_candidates"`
	Published          int      `json:"published"`
	Discarded          int      `json:"discarded"`
	Merged             int      `json:"merged"`
	JudgeSkipped       int      `json:"judge_skipped"`
	Estimate           Estimate `json:"estimate"`
	Provider           string   `json:"provider"`
	Calls              int      `json:"calls"`
	MaxCalls           int      `json:"max_calls,omitempty"`
	ElapsedMS          int64    `json:"elapsed_ms"`
	Warnings           []string `json:"warnings,omitempty"`
	// Issues é o que a publicação no Linear fez nesta rodada (nil em modo sombra).
	Issues *linear.PublishResult `json:"issues,omitempty"`
}

// Result de uma rodada.
type Result struct {
	RunID     int64
	Status    string
	Stats     Stats
	Cost      llm.NanoUSD
	Calls     int
	Elapsed   time.Duration
	NoChanges bool
	// PublishErr é a falha da publicação no Linear. A rodada continua succeeded: os achados
	// ficam pendentes e a próxima rodada tenta de novo.
	PublishErr error
}

// ErrEstimateOverBudget indica que a estimativa esperada passou do orçamento; nada foi chamado.
var ErrEstimateOverBudget = errors.New("estimativa da rodada acima do orçamento")

// Run executa uma rodada completa.
func (r *Runner) Run(ctx context.Context, o Options) (res Result, err error) {
	rc, ok := r.Cfg.Repo(o.Repo)
	if !ok {
		return res, fmt.Errorf("%w: %s", repo.ErrUnknownRepo, o.Repo)
	}
	budgetUSD := o.BudgetUSD
	if budgetUSD <= 0 {
		budgetUSD = r.Cfg.Budget.PerRunUSD
	}
	started := time.Now()
	ctx, span := r.Tracer.Start(ctx, "sentinela.run", trace.WithAttributes(telemetry.AttrRepo.String(rc.Name)))
	defer func() {
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()

	last, err := r.Store.Q.LastCompletedRun(ctx, rc.Name)
	hasLast := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return res, err
	}
	checkout, err := r.Git.Sync(ctx, rc.Name, rc.DefaultBranch)
	if err != nil {
		return res, err
	}
	stats := Stats{Mode: "full", HeadSHA: checkout.HeadSHA, Provider: r.Cfg.LLM.Provider}
	if r.Cfg.LLM.Provider != config.ProviderAnthropicAPI {
		stats.MaxCalls = r.Cfg.LLM.ClaudeCode.MaxCallsPerRun
		budgetUSD = 0 // dólar não se aplica: a rodada é limitada por chamadas
	}

	stats.BuildUnits = repo.BuildUnits(checkout.Dir)
	all, err := repo.MapPackages(checkout.Dir)
	if err != nil {
		return res, err
	}
	analyzed := all
	if hasLast && !o.Full {
		changed, cerr := r.Git.ChangedFiles(ctx, checkout, last.HeadSha.String)
		switch {
		case errors.Is(cerr, repo.ErrUnknownBase):
			stats.Warnings = append(stats.Warnings, "head_sha anterior não existe mais (force push?); análise completa")
		case cerr != nil:
			return res, cerr
		default:
			stats.Mode, stats.BaseSHA = "incremental", last.HeadSha.String
			analyzed = repo.FilterChanged(all, changed)
		}
	}
	co, err := codeownersFrom(checkout.Dir)
	if err != nil {
		return res, err
	}
	resolver := repo.NewResolver(r.Cfg, repo.StaticCodeowners{rc.Name: co})
	resolve := func(p string) (repo.Resolution, error) { return resolver.Resolve(rc.Name, p) }
	if stats.Mode == "incremental" && len(analyzed) == 0 {
		if o.EstimateOnly {
			return Result{NoChanges: true, Stats: stats}, nil
		}
		return r.noChanges(ctx, rc.Name, budgetUSD, stats, resolve, started)
	}
	sups, err := r.suppressions(ctx, rc.Name)
	if err != nil {
		return res, err
	}
	langs := map[string]bool{}
	for _, p := range all {
		langs[p.Language] = true
	}
	_, scanSpan := r.Tracer.Start(ctx, "scan.scanners")
	sigs, scanResults := r.Scanners.Run(ctx, checkout.Dir, langs)
	scanSpan.End()
	stats.Scanners, stats.ScanSignals = scanResults, len(sigs)

	src := &code.Source{
		Cfg: r.Cfg, Checkout: checkout, Resolver: resolver, Packages: analyzed, AllPackages: all,
		Edges: repo.DependencyGraph(checkout.Dir, all), ScanSignals: sigs, Suppressions: sups, Tracer: r.Tracer,
	}
	raws, err := src.Collect(ctx, time.Time{})
	if err != nil {
		return res, err
	}
	stats.PackagesTotal, stats.PackagesAnalyzed = len(all), len(raws)
	stats.PackagesIgnored = len(analyzed) - len(raws)

	est, err := r.estimate(src, raws)
	if err != nil {
		return res, err
	}
	stats.Estimate = est
	budget := llm.USD(budgetUSD)
	if o.EstimateOnly {
		return Result{Stats: stats}, nil
	}
	switch {
	case r.Cfg.LLM.Provider == config.ProviderAnthropicAPI && est.Expected > budget:
		return Result{Stats: stats}, fmt.Errorf("%w: esperado %s, orçamento %s", ErrEstimateOverBudget, est.Expected, budget)
	case r.Cfg.LLM.Provider != config.ProviderAnthropicAPI && est.ExpectedCalls > stats.MaxCalls:
		return Result{Stats: stats}, fmt.Errorf("%w: %d chamadas esperadas, limite %d", ErrEstimateOverBudget, est.ExpectedCalls, stats.MaxCalls)
	}

	run, err := r.Store.Q.CreateRun(ctx, db.CreateRunParams{
		Repo: rc.Name, BaseSha: text(stats.BaseSHA), HeadSha: text(checkout.HeadSHA),
		BudgetUsd: store.NanoUSDToNumeric(int64(budget)), Mode: stats.Mode,
	})
	if err != nil {
		return res, err
	}
	span.SetAttributes(telemetry.AttrRunID.Int64(run.ID))
	res = Result{RunID: run.ID, Stats: stats}
	if err := r.recordPrompts(ctx); err != nil {
		return res, r.finish(ctx, &res, "failed", err)
	}

	client, err := r.NewLLM(run.ID, budget)
	if err != nil {
		return res, r.finish(ctx, &res, "failed", err)
	}
	src.Client, src.Log = client, &itemLog{q: r.Store.Q, runID: run.ID}

	repoCtx, repoSpan := r.Tracer.Start(ctx, "repo "+rc.Name, trace.WithAttributes(telemetry.AttrRepo.String(rc.Name)))
	findings, ierr := src.InterpretAll(repoCtx, raws)
	res.Stats.Warnings = append(res.Stats.Warnings, src.Warnings...)
	for _, t := range src.Triage {
		if t.Deepen {
			res.Stats.Deepened++
		}
	}
	res.Stats.Proposed = len(findings)
	res.Stats.InvalidEvidence = src.InvalidEvidence
	for _, d := range src.DroppedEvidence {
		res.Stats.EvidenceDropped += len(d)
	}
	if ierr == nil {
		ierr = r.judgeAndStore(repoCtx, client, run.ID, src, findings, &res.Stats)
	}
	repoSpan.End()
	res.Cost, res.Calls = client.Budget().Spent(), client.Budget().Calls()
	res.Stats.Calls, res.Elapsed = res.Calls, time.Since(started)
	res.Stats.ElapsedMS = res.Elapsed.Milliseconds()
	switch {
	case errors.Is(ierr, llm.ErrBudgetExceeded):
		return res, r.finish(ctx, &res, "aborted_budget", ierr)
	case ierr != nil:
		return res, r.finish(ctx, &res, "failed", ierr)
	}
	r.publish(ctx, &res, rc.Name, resolve)
	return res, r.finish(ctx, &res, "succeeded", nil)
}

// publish publica os achados pendentes do repo e grava o resultado nas estatísticas.
func (r *Runner) publish(ctx context.Context, res *Result, repoName string, resolve linear.Resolve) {
	if r.Publisher == nil {
		return
	}
	pctx, span := r.Tracer.Start(ctx, "linear.publish", trace.WithAttributes(telemetry.AttrRepo.String(repoName)))
	defer span.End()
	pr, err := r.Publisher.Publish(pctx, repoName, res.RunID, resolve)
	res.Stats.Issues = &pr
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		res.PublishErr = err
		res.Stats.Warnings = append(res.Stats.Warnings, "publicação no Linear falhou (achados ficam pendentes): "+err.Error())
	}
}

// noChanges grava uma rodada sem custo: nada mudou desde o último head_sha.
func (r *Runner) noChanges(ctx context.Context, repoName string, budgetUSD float64, stats Stats, resolve linear.Resolve, started time.Time) (Result, error) {
	run, err := r.Store.Q.CreateRun(ctx, db.CreateRunParams{
		Repo: repoName, BaseSha: text(stats.BaseSHA), HeadSha: text(stats.HeadSHA),
		BudgetUsd: store.NanoUSDToNumeric(int64(llm.USD(budgetUSD))), Mode: "incremental",
	})
	if err != nil {
		return Result{}, err
	}
	res := Result{RunID: run.ID, Stats: stats, NoChanges: true}
	r.publish(ctx, &res, repoName, resolve) // pendentes de rodadas anteriores (limite por time)
	res.Elapsed = time.Since(started)
	res.Stats.ElapsedMS = res.Elapsed.Milliseconds()
	return res, r.finish(ctx, &res, "no_changes", nil)
}

func (r *Runner) finish(ctx context.Context, res *Result, status string, cause error) error {
	res.Status = status
	data, err := json.Marshal(res.Stats)
	if err != nil {
		return errors.Join(cause, err)
	}
	var msg pgtype.Text
	if cause != nil {
		msg = text(cause.Error())
	}
	// Grava o desfecho mesmo se o contexto da rodada foi cancelado.
	if err := r.Store.Q.FinishRun(context.WithoutCancel(ctx), db.FinishRunParams{ID: res.RunID, Status: status, Error: msg, Stats: data}); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

// judgeAndStore deduplica, julga e grava os achados aprovados.
// judgeOpts muda o destino dos achados aprovados: DryRun só registra a decisão do Judge (modo
// sombra das notas de review); OriginExecutionID liga o achado à execução que o originou.
type judgeOpts struct {
	DryRun            bool
	OriginExecutionID int64
}

func (r *Runner) judgeAndStore(ctx context.Context, client llm.Caller, runID int64, src *code.Source, findings []signals.Finding, st *Stats, opts ...judgeOpts) error {
	var jo judgeOpts
	if len(opts) > 0 {
		jo = opts[0]
	}
	log := &itemLog{q: r.Store.Q, runID: runID}
	examples, err := feedback.JudgeExamples(ctx, r.Store.Q, src.Checkout.Repo, r.Cfg.Feedback.MaxExamples)
	if err != nil {
		return err
	}
	thresholds, err := feedback.LoadThresholds(ctx, r.Store.Q, src.Checkout.Repo, r.Cfg.Discovery.MinConfidence)
	if err != nil {
		return err
	}
	prior, err := r.priorRejections(ctx, src.Checkout.Repo)
	if err != nil {
		return err
	}
	open, err := r.openFindings(ctx, src.Checkout.Repo)
	if err != nil {
		return err
	}
	merges, err := r.mergeGraph(ctx, src.Checkout.Repo, prior)
	if err != nil {
		return err
	}
	pool, err := r.locationPool(ctx, src.Checkout.Repo, src.Checkout.Dir, prior, merges)
	if err != nil {
		return err
	}
	located := map[string][]locationHit{}
	detected := map[string]map[string]judge.Overlap{}
	seen := map[string]bool{}
	vecs := map[string][]float32{}
	var items []agents.Item
	byKey := map[string]signals.Finding{}
	for _, f := range findings {
		skip := func(reason string) error {
			return log.Item(ctx, judge.Name, f.Fingerprint, "skipped", "", f, reason)
		}
		if seen[f.Fingerprint] {
			st.Duplicates++
			if err := skip("duplicado na própria rodada"); err != nil {
				return err
			}
			continue
		}
		seen[f.Fingerprint] = true
		if why, ok := code.SuppressedBy(f, src.Suppressions); ok {
			st.Suppressed++
			if err := skip(why); err != nil {
				return err
			}
			continue
		}
		if existing, err := r.Store.Q.FindingByFingerprint(ctx, f.Fingerprint); err == nil {
			st.Duplicates++
			if err := skip(fmt.Sprintf("fingerprint já registrado (achado %d, %s)", existing.ID, existing.State)); err != nil {
				return err
			}
			continue
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if p, ok := prior.byFingerprint[f.Fingerprint]; ok {
			st.Duplicates++
			if err := skip(fmt.Sprintf("o Judge já decidiu %s este fingerprint na rodada %d: %s", p.Decision, p.RunID, p.Reason)); err != nil {
				return err
			}
			continue
		}
		vec := r.Embedder.Embed(f, src.ProblemKeys[f.Fingerprint])
		vecs[f.Fingerprint] = vec
		similar, err := r.Store.Q.SimilarFindings(ctx, db.SimilarFindingsParams{Embedding: pgvector.NewVector(vec), Repo: f.Repo, MaxResults: 3})
		if err != nil {
			return err
		}
		if dup, ok := r.nearDuplicate(similar, vecs, f); ok {
			st.Duplicates++
			if err := skip(dup); err != nil {
				return err
			}
			continue
		}
		minConf, _ := thresholds.For(f.Module, f.Category)
		in := judge.Input{Finding: judgeView(f), Examples: examples, MinConfidence: minConf}
		ov := overlaps(f, open)
		detected[f.Fingerprint] = ov
		shown := map[string]bool{}
		for _, s := range similar {
			if merges.partOf(s.Fingerprint, f.Fingerprint) {
				continue
			}
			shown[s.Fingerprint] = true
			if o, ok := open[s.Fingerprint]; ok {
				in.Similar = append(in.Similar, similarFrom(o, s.Similarity, ov[s.Fingerprint]))
				continue
			}
			in.Similar = append(in.Similar, judge.Similar{Fingerprint: s.Fingerprint, Title: s.Title, Category: s.Category, State: s.State, Similarity: s.Similarity})
		}
		for _, fp := range sortedKeys(ov) {
			if !shown[fp] {
				// Fora dos mais próximos no pgvector, mas com critério ou evidência em comum.
				in.Similar = append(in.Similar, similarFrom(open[fp], 0, ov[fp]))
			}
		}
		in.Similar = append(in.Similar, prior.nearest(vec, 3, f.Fingerprint, merges)...)
		if sims, hits := pool.attach(f, in.Similar); len(hits) > 0 {
			in.Similar, located[f.Fingerprint] = sims, hits
			st.LocationCandidates++
		}
		models := r.Cfg.ModelsFor(f.Repo, f.Routing.Team)
		req, err := judge.Request(in, judge.Options{Model: models.Judge})
		if err != nil {
			return err
		}
		items = append(items, agents.Item{Key: f.Fingerprint, Request: req, Fallback: models.RefusalFallback})
		byKey[f.Fingerprint] = f
	}

	outcomes, runErr := agents.RunAll(ctx, client, r.Tracer, judge.Name, items, r.Cfg.Models.Refusal)
	if runErr != nil && !errors.Is(runErr, llm.ErrBudgetExceeded) {
		return runErr
	}
	// Primeiro voto de cada achado; os que ficam perto do limiar votam de novo (mediana).
	first := map[string]judge.Verdict{}
	reqs := map[string]agents.Item{}
	for _, it := range items {
		f, o := byKey[it.Key], outcomes[it.Key]
		if o.Skipped {
			st.JudgeSkipped++
			if err := log.Item(ctx, judge.Name, it.Key, "skipped", o.Response.Model, f, o.Reason); err != nil {
				return err
			}
			continue
		}
		v, err := judge.Parse(o.Response)
		if err != nil {
			st.JudgeSkipped++
			if err := log.Item(ctx, judge.Name, it.Key, "failed", o.Response.Model, f, err.Error()); err != nil {
				return err
			}
			continue
		}
		first[it.Key], reqs[it.Key] = v, it
	}
	votes, voteErr := r.revote(ctx, client, reqs, first, func(key string, _ judge.Verdict) float64 {
		minConf, _ := thresholds.For(byKey[key].Module, byKey[key].Category)
		return minConf
	})
	if voteErr != nil && runErr == nil {
		runErr = voteErr
	}
	for _, it := range items {
		v, ok := first[it.Key]
		if !ok {
			continue
		}
		f, o := byKey[it.Key], outcomes[it.Key]
		if vs := votes[it.Key]; len(vs) > 1 {
			v = MedianVote(vs)
		}
		minConf, thFrom := thresholds.For(f.Module, f.Category)
		decision, th := Decide(minConf, v)
		out := map[string]any{"verdict": v, "decision": decision, "question": v.Premise != "" && decision == judge.Publish, "min_confidence": minConf, "threshold": thFrom, "dry_run": jo.DryRun, "effective_threshold": th}
		if vs := votes[it.Key]; len(vs) > 1 {
			out["votes"] = vs
		}
		if decision == judge.Merge && v.MergeWith != "" {
			if mc, err := r.MergeDirection(ctx, f.Fingerprint, v.MergeWith); err == nil {
				out["merge_direction"] = mc
			}
		}
		if hits := located[f.Fingerprint]; len(hits) > 0 {
			out["location"] = hits
		}
		if d := src.DroppedEvidence[f.Fingerprint]; len(d) > 0 {
			out["evidence_dropped"] = d
		}
		if decision == judge.Publish {
			scope := applyScope(&f, v, detected[f.Fingerprint], open)
			out["scope"], out["related_to"] = scope, agents.Slice(scope.RelatedTo)
			if scope.MergeWith != "" {
				decision, out["decision"] = judge.Merge, judge.Merge
				v.Reason += fmt.Sprintf(" [Sentinela: todos os critérios já estão em %s; tratado como duplicado.]", scope.MergeWith)
				out["verdict"] = v
			} else if scope.Narrowed || len(scope.Removed) > 0 {
				st.Narrowed++
			}
		}
		out["finding"] = f
		if err := log.Item(ctx, judge.Name, it.Key, "ok", o.Response.Model, out, v.Reason); err != nil {
			return err
		}
		switch decision {
		case judge.Publish:
			f.Context = v.ApplyPremise(f.Context)
			if jo.DryRun {
				st.Published++ // publicaria; em modo sombra nada vai para findings
				continue
			}
			if err := r.insert(ctx, runID, f, vecs[f.Fingerprint], v, jo.OriginExecutionID); err != nil {
				return err
			}
			st.Published++
		case judge.Merge:
			st.Merged++
		default:
			st.Discarded++
		}
	}
	return runErr
}

// nearDuplicate compara com achados já gravados e com os da própria rodada.
func (r *Runner) nearDuplicate(similar []db.SimilarFindingsRow, vecs map[string][]float32, f signals.Finding) (string, bool) {
	th := r.Cfg.Discovery.SimilarityThreshold
	for _, s := range similar {
		if s.Similarity >= th {
			return fmt.Sprintf("similar a %s (%.2f, %s): %s", s.Fingerprint, s.Similarity, s.State, s.Title), true
		}
	}
	for fp, v := range vecs {
		if fp == f.Fingerprint {
			continue
		}
		if sim := Cosine(v, vecs[f.Fingerprint]); sim >= th {
			return fmt.Sprintf("similar a %s da própria rodada (%.2f)", fp, sim), true
		}
	}
	return "", false
}

func (r *Runner) insert(ctx context.Context, runID int64, f signals.Finding, vec []float32, v judge.Verdict, origin int64) error {
	routing, err := json.Marshal(f.Routing)
	if err != nil {
		return err
	}
	evidence, err := json.Marshal(f.Evidence)
	if err != nil {
		return err
	}
	sigs, err := json.Marshal(agents.Slice(f.Signals))
	if err != nil {
		return err
	}
	_, err = r.Store.Q.InsertFinding(ctx, db.InsertFindingParams{
		RunID: pgtype.Int8{Int64: runID, Valid: true}, Fingerprint: f.Fingerprint, Source: f.Source,
		Repo: f.Repo, Module: f.Module, Owners: agents.Slice(f.Owners), Routing: routing,
		Category: f.Category, Severity: f.Severity, Confidence: float32(f.Confidence),
		Title: f.Title, Context: f.Context, Evidence: evidence, Signals: sigs, ProposedFix: f.ProposedFix,
		AcceptanceCriteria: agents.Slice(f.AcceptanceCriteria), ScopePaths: agents.Slice(f.ScopePaths),
		Embedding: pgvector.NewVector(vec), RelatedModules: agents.Slice(f.RelatedModules),
		JudgeConfidence: pgtype.Float4{Float32: float32(v.Confidence), Valid: true}, JudgeReason: text(v.Reason),
		OriginExecutionID: pgtype.Int8{Int64: origin, Valid: origin != 0},
		Premise:           text(strings.TrimSpace(v.Premise)),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // ON CONFLICT DO NOTHING: outra rodada gravou o mesmo fingerprint
	}
	return err
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func judgeView(f signals.Finding) judge.Finding {
	j := judge.Finding{
		Source: f.Source, Repo: f.Repo, Module: f.Module, Category: f.Category, Severity: f.Severity,
		Confidence: f.Confidence, Title: f.Title, Context: f.Context, ProposedFix: f.ProposedFix,
		AcceptanceCriteria: f.AcceptanceCriteria, RelatedModules: f.RelatedModules, Fingerprint: f.Fingerprint,
	}
	for _, e := range f.Evidence {
		j.Evidence = append(j.Evidence, judge.Evidence{File: e.File, Line: e.Line, Snippet: e.Snippet})
	}
	return j
}

// priorDecision é um achado que o Judge descartou ou mesclou numa rodada anterior.
type priorDecision struct {
	RunID       int64
	Fingerprint string
	Title       string
	Category    string
	Decision    string
	Reason      string
	Module      string
	Evidence    []signals.Evidence
	// MergedInto é o achado em que o Judge mesclou este ('' se não foi mescla).
	MergedInto string
	vec        []float32
}

type priorDecisions struct {
	list          []priorDecision
	byFingerprint map[string]priorDecision
}

// maxPriorDecisions limita quantas decisões antigas entram na comparação (as mais recentes).
const maxPriorDecisions = 300

// priorRejections lê de run_items os achados que o Judge já descartou ou mesclou no repo.
// Achados descartados não vão para a tabela findings; sem isto, um problema descartado
// volta com outras palavras e o Judge, que não é determinístico, pode publicá-lo na rodada
// seguinte (visto no app-java-angular, rodadas 7 e 8).
func (r *Runner) priorRejections(ctx context.Context, repoName string) (priorDecisions, error) {
	out := priorDecisions{byFingerprint: map[string]priorDecision{}}
	rows, err := r.Store.Q.PriorJudgeRejections(ctx, db.PriorJudgeRejectionsParams{Repo: repoName, Limit: maxPriorDecisions})
	if err != nil {
		return out, err
	}
	for _, row := range rows {
		var o struct {
			Decision       string          `json:"decision"`
			Finding        signals.Finding `json:"finding"`
			Verdict        judge.Verdict   `json:"verdict"`
			MergeDirection *MergeChoice    `json:"merge_direction"`
		}
		if json.Unmarshal(row.Output, &o) != nil || o.Finding.Title == "" {
			continue
		}
		p := priorDecision{
			RunID: row.RunID, Fingerprint: row.ItemKey, Title: o.Finding.Title, Category: o.Finding.Category,
			Decision: o.Decision, Reason: o.Verdict.Reason, Module: o.Finding.Module, Evidence: o.Finding.Evidence,
			MergedInto: mergeTarget(row.ItemKey, o.Decision, o.Verdict.MergeWith, o.MergeDirection),
			vec:        r.Embedder.Embed(o.Finding, o.Finding.Title),
		}
		if _, dup := out.byFingerprint[p.Fingerprint]; !dup {
			out.byFingerprint[p.Fingerprint] = p
			out.list = append(out.list, p)
		}
	}
	return out, nil
}

// mergeTarget é o achado em que uma mescla do Judge pôs fp: o absorvedor da direção
// determinística, quando fp é o absorvido; sem direção gravada, o merge_with do veredito. Se fp
// absorveu o outro, ele não foi mesclado em nada.
func mergeTarget(fp, decision, mergeWith string, dir *MergeChoice) string {
	if decision != judge.Merge {
		return ""
	}
	if dir != nil && dir.Absorber != "" {
		if dir.Absorbed == fp {
			return dir.Absorber
		}
		return ""
	}
	return mergeWith
}

// mergeGraph liga cada achado mesclado àquele em que foi mesclado: duplicatas humanas
// (merged_into, que valem primeiro) e mesclas do Judge (run_items).
type mergeGraph map[string]string

func (r *Runner) mergeGraph(ctx context.Context, repoName string, prior priorDecisions) (mergeGraph, error) {
	rows, err := r.Store.Q.MergedFindings(ctx, repoName)
	if err != nil {
		return nil, err
	}
	g := mergeGraph{}
	for _, row := range rows {
		g[row.Fingerprint] = row.MergedInto
	}
	for _, d := range prior.list {
		if _, human := g[d.Fingerprint]; !human && d.MergedInto != "" {
			g[d.Fingerprint] = d.MergedInto
		}
	}
	return g, nil
}

// partOf diz se fp foi mesclado em self, direta ou indiretamente (fp → X → self). Um achado
// assim é parte de self, não outro achado: mostrado ao Judge como similar de self, ele leva o
// Judge a ver em self uma duplicata de uma parte dele mesmo (EX-6 e EX-7 no rejudge v7 de
// 2026-10-10, ADR 0025).
func (g mergeGraph) partOf(fp, self string) bool {
	seen := map[string]bool{}
	for fp != "" && !seen[fp] {
		seen[fp] = true
		if fp = g[fp]; fp == self {
			return true
		}
	}
	return false
}

// nearest devolve as k decisões anteriores mais parecidas, como similares para o Judge,
// com estado "discarded" ou "merged", sem as que são parte do próprio achado (self).
func (p priorDecisions) nearest(vec []float32, k int, self string, merges mergeGraph) []judge.Similar {
	type scored struct {
		d   priorDecision
		sim float64
	}
	var all []scored
	for _, d := range p.list {
		if merges.partOf(d.Fingerprint, self) {
			continue
		}
		if sim := Cosine(vec, d.vec); sim > 0.3 {
			all = append(all, scored{d, sim})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].sim > all[j].sim })
	var out []judge.Similar
	for i := 0; i < len(all) && i < k; i++ {
		state := "discarded"
		if all[i].d.Decision == judge.Merge {
			state = "merged"
		}
		out = append(out, judge.Similar{Fingerprint: all[i].d.Fingerprint, Title: all[i].d.Title, Category: all[i].d.Category, State: state, Similarity: all[i].sim})
	}
	return out
}

func (r *Runner) suppressions(ctx context.Context, repoName string) ([]agents.Suppression, error) {
	rows, err := r.Store.Q.ActiveSuppressions(ctx, repoName)
	if err != nil {
		return nil, err
	}
	out := make([]agents.Suppression, 0, len(rows))
	for _, s := range rows {
		out = append(out, agents.Suppression{Fingerprint: s.Fingerprint.String, Pattern: s.Pattern.String, Category: s.Category.String, Reason: s.Reason})
	}
	return out, nil
}

func (r *Runner) recordPrompts(ctx context.Context) error {
	for _, p := range []agents.Prompt{triager.PromptVersion(), analyst.PromptVersion(), architect.PromptVersion(), judge.PromptVersion()} {
		if err := r.Store.Q.UpsertPromptVersion(ctx, db.UpsertPromptVersionParams{Agent: p.Agent, Version: p.Version, Sha256: p.SHA256}); err != nil {
			return err
		}
	}
	return nil
}

func codeownersFrom(dir string) (*repo.Codeowners, error) {
	for _, p := range repo.CodeownersPaths {
		f, err := os.Open(filepath.Join(dir, filepath.FromSlash(p))) //nolint:gosec // caminhos fixos do GitHub
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		co, perr := repo.ParseCodeowners(f)
		_ = f.Close()
		return co, perr
	}
	return nil, nil
}

// itemLog grava run_items.
type itemLog struct {
	q     *db.Queries
	runID int64
}

func (l *itemLog) Item(ctx context.Context, agent, key, status, model string, output any, reason string) error {
	var data []byte
	if output != nil {
		var err error
		if data, err = json.Marshal(output); err != nil {
			return err
		}
	}
	return l.q.InsertRunItem(ctx, db.InsertRunItemParams{
		RunID: l.runID, Agent: agent, ItemKey: key, Status: status, Model: text(model), Output: data, Reason: text(reason),
	})
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }
