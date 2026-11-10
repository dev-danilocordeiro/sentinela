package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/pgvector/pgvector-go"

	"github.com/dev-danilocordeiro/sentinela/internal/agents"
	"github.com/dev-danilocordeiro/sentinela/internal/agents/judge"
	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/feedback"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
	"github.com/dev-danilocordeiro/sentinela/internal/signals"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// RejudgeItem compara a decisão registrada de um achado com a do Judge atual (ADR 0020).
type RejudgeItem struct {
	Repo        string  `json:"repo"`
	Fingerprint string  `json:"fingerprint"`
	Title       string  `json:"title"`
	Issue       string  `json:"issue,omitempty"`
	Before      string  `json:"before"`
	BeforeConf  float64 `json:"before_conf"`
	After       string  `json:"after"` // publish | question | discard | merge | skipped
	AfterConf   float64 `json:"after_conf"`
	Threshold   float64 `json:"threshold"`
	Premise     string  `json:"premise,omitempty"`
	// DependsOnProduct é a resposta do Judge a "depende de uma decisão de produto?".
	DependsOnProduct bool            `json:"depends_on_product"`
	Reason           string          `json:"reason"`
	Votes            []judge.Verdict `json:"votes,omitempty"`
	MergeWith        string          `json:"merge_with,omitempty"`
	Merge            *MergeChoice    `json:"merge,omitempty"`
	// Applied: com apply, o achado descartado ou mesclado que agora publica (ou vira pergunta) foi
	// gravado como achado aberto, para a publicação normal.
	Applied bool `json:"applied,omitempty"`
}

// Rejudge julga de novo, com o Judge e as regras atuais, cada achado já decidido dos repos: os
// gravados em findings e os descartados ou mesclados em run_items (a decisão mais recente).
// Registra a rodada (mode rejudge), as chamadas e os votos (agente "rejudge", que a memória de
// decisões anteriores não lê). Sem apply, não grava achado nem publica. Com apply, grava como
// achado aberto (com a premissa, se houver) o que estava descartado ou mesclado e agora publica
// ou vira pergunta; a publicação normal o leva ao Linear. Os já gravados não mudam: a decisão
// sobre eles é do time.
func (r *Runner) Rejudge(ctx context.Context, repos []string, only []string, apply bool) (int64, []RejudgeItem, error) {
	items, findings, err := r.decided(ctx, repos)
	if err == nil && len(only) > 0 {
		keep := map[string]bool{}
		for _, o := range only {
			keep[o] = true
		}
		var sel []RejudgeItem
		for _, it := range items {
			if keep[it.Fingerprint] || (it.Issue != "" && keep[it.Issue]) {
				sel = append(sel, it)
			}
		}
		items = sel
	}
	if err != nil || len(items) == 0 {
		return 0, items, err
	}
	budget := llm.USD(r.Cfg.Budget.PerRunUSD)
	if r.Cfg.LLM.Provider != config.ProviderAnthropicAPI {
		budget = 0 // claude_code: limitado por chamadas
	}
	run, err := r.Store.Q.CreateRun(ctx, db.CreateRunParams{Repo: repos[0], Mode: "rejudge", BudgetUsd: store.NanoUSDToNumeric(int64(budget))})
	if err != nil {
		return 0, nil, err
	}
	client, err := r.NewLLM(run.ID, budget)
	if err != nil {
		return run.ID, nil, err
	}
	reqs := map[string]agents.Item{}
	var calls []agents.Item
	for _, it := range items {
		f := findings[it.Fingerprint]
		in, err := r.rejudgeInput(ctx, f)
		if err != nil {
			return run.ID, nil, err
		}
		models := r.Cfg.ModelsFor(f.Repo, f.Routing.Team)
		req, err := judge.Request(in, judge.Options{Model: models.Judge})
		if err != nil {
			return run.ID, nil, err
		}
		item := agents.Item{Key: it.Fingerprint, Request: req, Fallback: models.RefusalFallback}
		reqs[it.Fingerprint] = item
		calls = append(calls, item)
	}
	outcomes, runErr := agents.RunAll(ctx, client, r.Tracer, judge.Name, calls, r.Cfg.Models.Refusal)
	if runErr != nil && !errors.Is(runErr, llm.ErrBudgetExceeded) {
		return run.ID, nil, runErr
	}
	first := map[string]judge.Verdict{}
	for i := range items {
		o := outcomes[items[i].Fingerprint]
		if o.Skipped {
			items[i].After, items[i].Reason = "skipped", o.Reason
			continue
		}
		v, err := judge.Parse(o.Response)
		if err != nil {
			items[i].After, items[i].Reason = "skipped", err.Error()
			continue
		}
		first[items[i].Fingerprint] = v
	}
	minConf := func(f signals.Finding) float64 {
		th, _ := feedback.LoadThresholds(ctx, r.Store.Q, f.Repo, r.Cfg.Discovery.MinConfidence)
		m, _ := th.For(f.Module, f.Category)
		return m
	}
	votes, voteErr := r.revote(ctx, client, reqs, first, func(key string, _ judge.Verdict) float64 {
		return minConf(findings[key])
	})
	log := &itemLog{q: r.Store.Q, runID: run.ID}
	for i := range items {
		it := &items[i]
		v, ok := first[it.Fingerprint]
		if !ok {
			continue
		}
		if vs := votes[it.Fingerprint]; len(vs) > 1 {
			v, it.Votes = MedianVote(vs), vs
		}
		decision, th := decide(minConf(findings[it.Fingerprint]), v)
		it.Threshold = th
		it.After, it.AfterConf, it.Reason, it.Premise, it.MergeWith = decision, v.Confidence, v.Reason, v.Premise, v.MergeWith
		it.DependsOnProduct = v.ProductDecision.Depends
		switch {
		case v.Premise != "" && decision == judge.Publish:
			it.After = Question
		case decision == judge.Discard && v.Decision == judge.Publish:
			it.After = judge.Discard + " (abaixo do limiar)"
		case decision == judge.Merge:
			if mc, err := r.MergeDirection(ctx, it.Fingerprint, v.MergeWith); err == nil {
				it.Merge = &mc
			}
		}
		if apply && decision == judge.Publish && it.Before != judge.Publish {
			f := findings[it.Fingerprint]
			f.Context = v.ApplyPremise(f.Context)
			if err := r.insert(ctx, run.ID, f, r.Embedder.Embed(f, f.Title), v, 0); err != nil {
				return run.ID, items, err
			}
			it.Applied = true
		}
		if err := log.Item(ctx, "rejudge", it.Fingerprint, "ok", "", it, v.Reason); err != nil {
			return run.ID, items, err
		}
	}
	status := "succeeded"
	if runErr == nil {
		runErr = voteErr
	}
	if runErr != nil {
		status = "failed"
	}
	res := Result{RunID: run.ID, Stats: Stats{Mode: "rejudge", Provider: r.Cfg.LLM.Provider, Calls: client.Budget().Calls()}}
	return run.ID, items, r.finish(ctx, &res, status, runErr)
}

// decided carrega os achados já decididos dos repos, com a decisão registrada.
func (r *Runner) decided(ctx context.Context, repos []string) ([]RejudgeItem, map[string]signals.Finding, error) {
	findings := map[string]signals.Finding{}
	var items []RejudgeItem
	rows, err := r.Store.Pool.Query(ctx, `
		SELECT DISTINCT ON (ri.item_key) ar.repo, ri.item_key, ri.output
		FROM run_items ri JOIN analysis_runs ar ON ar.id = ri.run_id
		WHERE ri.agent = 'judge' AND ri.status = 'ok' AND ar.mode IN ('full', 'incremental') AND ar.repo = ANY($1)
		ORDER BY ri.item_key, ri.id DESC`, repos)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var repoName, fp string
		var out []byte
		if err := rows.Scan(&repoName, &fp, &out); err != nil {
			rows.Close()
			return nil, nil, err
		}
		var o struct {
			Decision string          `json:"decision"`
			Finding  signals.Finding `json:"finding"`
			Verdict  judge.Verdict   `json:"verdict"`
		}
		if json.Unmarshal(out, &o) != nil || o.Finding.Title == "" {
			continue
		}
		findings[fp] = o.Finding
		items = append(items, RejudgeItem{Repo: repoName, Fingerprint: fp, Title: o.Finding.Title, Before: o.Decision, BeforeConf: o.Verdict.Confidence})
	}
	rows.Close()
	frows, err := r.Store.Pool.Query(ctx, `SELECT id FROM findings WHERE source = 'code_analysis' AND merged_into IS NULL AND repo = ANY($1)`, repos)
	if err != nil {
		return nil, nil, err
	}
	var ids []int64
	for frows.Next() {
		var id int64
		_ = frows.Scan(&id)
		ids = append(ids, id)
	}
	frows.Close()
	issue := map[string]string{}
	for _, id := range ids {
		f, err := r.Store.Q.GetFinding(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		if li, err := r.Store.Q.PrimaryIssueForFinding(ctx, id); err == nil {
			issue[f.Fingerprint] = li.Identifier
		}
		if _, ok := findings[f.Fingerprint]; ok {
			continue
		}
		sf := signals.Finding{Source: f.Source, Repo: f.Repo, Module: f.Module, Category: f.Category, Severity: f.Severity,
			Confidence: float64(f.Confidence), Title: f.Title, Context: f.Context, ProposedFix: f.ProposedFix,
			AcceptanceCriteria: f.AcceptanceCriteria, ScopePaths: f.ScopePaths, RelatedModules: f.RelatedModules, Fingerprint: f.Fingerprint}
		_ = json.Unmarshal(f.Evidence, &sf.Evidence)
		_ = json.Unmarshal(f.Routing, &sf.Routing)
		findings[f.Fingerprint] = sf
		items = append(items, RejudgeItem{Repo: f.Repo, Fingerprint: f.Fingerprint, Title: f.Title, Before: "publish", BeforeConf: float64(f.JudgeConfidence.Float32)})
	}
	for i := range items {
		items[i].Issue = issue[items[i].Fingerprint]
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Repo+items[i].Title < items[j].Repo+items[j].Title })
	return items, findings, nil
}

// rejudgeInput monta a entrada do Judge como judgeAndStore, sem o próprio achado nos similares nem
// nas decisões anteriores.
func (r *Runner) rejudgeInput(ctx context.Context, f signals.Finding) (judge.Input, error) {
	examples, err := feedback.JudgeExamples(ctx, r.Store.Q, f.Repo, r.Cfg.Feedback.MaxExamples)
	if err != nil {
		return judge.Input{}, err
	}
	th, err := feedback.LoadThresholds(ctx, r.Store.Q, f.Repo, r.Cfg.Discovery.MinConfidence)
	if err != nil {
		return judge.Input{}, err
	}
	prior, err := r.priorRejections(ctx, f.Repo)
	if err != nil {
		return judge.Input{}, err
	}
	delete(prior.byFingerprint, f.Fingerprint)
	var list []priorDecision
	for _, p := range prior.list {
		if p.Fingerprint != f.Fingerprint {
			list = append(list, p)
		}
	}
	prior.list = list
	open, err := r.openFindings(ctx, f.Repo)
	if err != nil {
		return judge.Input{}, err
	}
	merges, err := r.mergeGraph(ctx, f.Repo, prior)
	if err != nil {
		return judge.Input{}, err
	}
	delete(open, f.Fingerprint)
	vec := r.Embedder.Embed(f, f.Title)
	similar, err := r.Store.Q.SimilarFindings(ctx, db.SimilarFindingsParams{Embedding: pgvector.NewVector(vec), Repo: f.Repo, MaxResults: 4})
	if err != nil {
		return judge.Input{}, err
	}
	minConf, _ := th.For(f.Module, f.Category)
	in := judge.Input{Finding: judgeView(f), Examples: examples, MinConfidence: minConf}
	ov := overlaps(f, open)
	shown := map[string]bool{}
	for _, s := range similar {
		if s.Fingerprint == f.Fingerprint || merges.partOf(s.Fingerprint, f.Fingerprint) || len(in.Similar) >= 3 {
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
			in.Similar = append(in.Similar, similarFrom(open[fp], 0, ov[fp]))
		}
	}
	in.Similar = append(in.Similar, prior.nearest(vec, 3, f.Fingerprint, merges)...)
	pool, err := r.locationPool(ctx, f.Repo, r.checkoutDir(f.Repo), prior, merges)
	if err != nil {
		return judge.Input{}, err
	}
	in.Similar, _ = pool.attach(f, in.Similar)
	return in, nil
}
