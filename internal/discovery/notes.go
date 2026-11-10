package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dev-danilocordeiro/sentinela/internal/agents"
	"github.com/dev-danilocordeiro/sentinela/internal/agents/notes"
	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/signals"
	"github.com/dev-danilocordeiro/sentinela/internal/signals/code"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// NotesOptions configura uma rodada de notas de review.
type NotesOptions struct {
	ExecutionID int64
	Shadow      bool // só registra o que o Judge decidiria; nada vai para findings nem para o Linear
}

// NoteDecision é o destino de cada achado candidato, para o relatório da rodada.
type NoteDecision struct {
	Notes       []string `json:"notes"` // ids e textos das notas de origem
	Title       string   `json:"title"`
	Fingerprint string   `json:"fingerprint"`
	Category    string   `json:"category"`
	Severity    string   `json:"severity"`
	Team        string   `json:"team"`
	Decision    string   `json:"decision"` // publish | discard | merge | skipped
	Confidence  float64  `json:"confidence"`
	Reason      string   `json:"reason"`
	Premise     string   `json:"premise,omitempty"`
	MergeWith   string   `json:"merge_with,omitempty"`
	Similar     []string `json:"similar,omitempty"`
}

// NoteDrop é uma nota que não virou achado.
type NoteDrop struct {
	Note   notes.Note `json:"note"`
	Reason string     `json:"reason"`
}

// NotesResult é o que uma rodada de notas produziu.
type NotesResult struct {
	RunID   int64
	Notes   []notes.Note
	Ignored []string // itens de review que não são observação fora do escopo
	// Dropped são as notas que o intérprete não transformou em achado, com o motivo dele.
	Dropped   []NoteDrop
	Decisions []NoteDecision
	Warnings  []string
	Calls     int
	Cost      llm.NanoUSD
	Elapsed   time.Duration
}

// excerptRadius é quantas linhas em volta de cada referência vão para o intérprete.
const excerptRadius = 12

// RunReviewNotes transforma as observações que não bloqueiam dos reviewers de uma execução em
// achados candidatos (source review_note), com a mesma validação de evidência, roteamento,
// deduplicação e Judge dos achados de código, ligados à execução de origem (ADR 0019).
func (r *Runner) RunReviewNotes(ctx context.Context, o NotesOptions) (NotesResult, error) {
	var res NotesResult
	started := time.Now()
	e, err := r.Store.Q.GetExecution(ctx, o.ExecutionID)
	if err != nil {
		return res, fmt.Errorf("execução %d: %w", o.ExecutionID, err)
	}
	rc, ok := r.Cfg.Repo(e.Repo)
	if !ok {
		return res, fmt.Errorf("%w: %s", repo.ErrUnknownRepo, e.Repo)
	}
	reviews, err := r.Store.Q.ReviewsByExecution(ctx, e.ID)
	if err != nil {
		return res, err
	}
	for _, rv := range reviews {
		var items []string
		_ = json.Unmarshal(rv.Findings, &items)
		for _, it := range items {
			text, ok := notes.IsNote(it)
			if !ok {
				res.Ignored = append(res.Ignored, fmt.Sprintf("%s/r%d: %s", rv.Lens, rv.Round, it))
				continue
			}
			res.Notes = append(res.Notes, notes.Note{ID: fmt.Sprintf("note:%d", len(res.Notes)+1), Lens: rv.Lens, Round: int(rv.Round), Text: text})
		}
	}
	if len(res.Notes) == 0 {
		return res, nil
	}
	origin, err := r.Store.Q.GetFinding(ctx, e.FindingID)
	if err != nil {
		return res, err
	}

	checkout, err := r.Git.Sync(ctx, rc.Name, rc.DefaultBranch)
	if err != nil {
		return res, err
	}
	all, err := repo.MapPackages(checkout.Dir)
	if err != nil {
		return res, err
	}
	co, err := codeownersFrom(checkout.Dir)
	if err != nil {
		return res, err
	}
	resolver := repo.NewResolver(r.Cfg, repo.StaticCodeowners{rc.Name: co})
	sups, err := r.suppressions(ctx, rc.Name)
	if err != nil {
		return res, err
	}

	in := notes.Input{
		Repo: rc.Name, Notes: res.Notes, MaxFindings: r.Cfg.Discovery.MaxFindingsPerPackage,
		Execution: notes.Execution{
			Issue: e.IssueIdentifier, Title: origin.Title, PRURL: e.PrUrl.String, HeadSHA: checkout.HeadSHA,
			ScopePaths: origin.ScopePaths, ProposedFix: origin.ProposedFix,
		},
		Excerpts: excerpts(checkout.Dir, res.Notes),
	}
	stats := Stats{Mode: "review_note", HeadSHA: checkout.HeadSHA, Provider: r.Cfg.LLM.Provider, PackagesTotal: len(all)}
	budget := llm.USD(r.Cfg.Budget.PerRunUSD)
	if r.Cfg.LLM.Provider != config.ProviderAnthropicAPI {
		stats.MaxCalls, budget = r.Cfg.LLM.ClaudeCode.MaxCallsPerRun, 0
	}
	run, err := r.Store.Q.CreateRun(ctx, db.CreateRunParams{Repo: rc.Name, HeadSha: text(checkout.HeadSHA), BudgetUsd: store.NanoUSDToNumeric(int64(budget)), Mode: "review_note"})
	if err != nil {
		return res, err
	}
	res.RunID = run.ID
	result := Result{RunID: run.ID, Stats: stats}
	if err := r.recordPrompts(ctx); err != nil {
		return res, r.finish(ctx, &result, "failed", err)
	}
	client, err := r.NewLLM(run.ID, budget)
	if err != nil {
		return res, r.finish(ctx, &result, "failed", err)
	}
	log := &itemLog{q: r.Store.Q, runID: run.ID}
	src := &code.Source{Cfg: r.Cfg, Checkout: checkout, Resolver: resolver, AllPackages: all, Suppressions: sups, Client: client, Tracer: r.Tracer, Log: log}

	// O intérprete usa os modelos do time dono do achado que originou o PR.
	var originRouting repo.Routing
	_ = json.Unmarshal(origin.Routing, &originRouting)
	findings, err := r.interpretNotes(ctx, client, src, log, in, originRouting.Team, &res)
	result.Stats.Proposed = len(findings)
	result.Stats.InvalidEvidence = src.InvalidEvidence
	if err == nil {
		err = r.judgeAndStore(ctx, client, run.ID, src, findings, &result.Stats, judgeOpts{DryRun: o.Shadow, OriginExecutionID: e.ID})
	}
	res.Cost, res.Calls = client.Budget().Spent(), client.Budget().Calls()
	result.Cost, result.Calls = res.Cost, res.Calls
	result.Stats.Calls = res.Calls
	res.Elapsed = time.Since(started)
	result.Stats.ElapsedMS = res.Elapsed.Milliseconds()
	result.Stats.Warnings = append(result.Stats.Warnings, res.Warnings...)
	switch {
	case errors.Is(err, llm.ErrBudgetExceeded):
		return res, r.finish(ctx, &result, "aborted_budget", err)
	case err != nil:
		return res, r.finish(ctx, &result, "failed", err)
	}
	if err := r.collectDecisions(ctx, run.ID, findings, res.Notes, &res); err != nil {
		return res, err
	}
	return res, r.finish(ctx, &result, "succeeded", nil)
}

// interpretNotes chama o intérprete e converte cada achado com as regras dos achados de código.
func (r *Runner) interpretNotes(ctx context.Context, client llm.Caller, src *code.Source, log *itemLog, in notes.Input, team string, res *NotesResult) ([]signals.Finding, error) {
	pv := notes.PromptVersion()
	if err := r.Store.Q.UpsertPromptVersion(ctx, db.UpsertPromptVersionParams{Agent: pv.Agent, Version: pv.Version, Sha256: pv.SHA256}); err != nil {
		return nil, err
	}
	models := r.Cfg.ModelsFor(in.Repo, team)
	req, err := notes.Request(in, notes.Options{Model: models.Notes})
	if err != nil {
		return nil, err
	}
	const key = "notes"
	outcomes, runErr := agents.RunAll(ctx, client, r.Tracer, notes.Name, []agents.Item{{Key: key, Request: req, Fallback: models.RefusalFallback}}, r.Cfg.Models.Refusal)
	if runErr != nil && !errors.Is(runErr, llm.ErrBudgetExceeded) {
		return nil, runErr
	}
	o := outcomes[key]
	if o.Skipped {
		return nil, log.Item(ctx, notes.Name, key, "skipped", o.Response.Model, in, o.Reason)
	}
	out, warnings, err := notes.Parse(o.Response, in)
	if err != nil {
		_ = log.Item(ctx, notes.Name, key, "failed", o.Response.Model, nil, err.Error())
		return nil, err
	}
	res.Warnings = append(res.Warnings, warnings...)
	if err := log.Item(ctx, notes.Name, key, "ok", o.Response.Model, map[string]any{"input": in, "output": out}, ""); err != nil {
		return nil, err
	}
	byID := map[string]notes.Note{}
	for _, n := range in.Notes {
		byID[n.ID] = n
	}
	for _, ig := range out.Ignored {
		res.Dropped = append(res.Dropped, NoteDrop{Note: byID[ig.ID], Reason: ig.Reason})
	}
	var findings []signals.Finding
	for i, fo := range out.Findings {
		var refs []signals.SignalRef
		for _, id := range fo.SignalRefs {
			n := byID[id]
			refs = append(refs, signals.SignalRef{Kind: signals.SourceReviewNote, Ref: fmt.Sprintf("execution:%s/round:%d/%s/%s", in.Execution.Issue, n.Round, n.Lens, id), Summary: n.Text})
		}
		f, reason := src.FromModel(fo, signals.SourceReviewNote, refs)
		if reason != "" {
			if err := log.Item(ctx, notes.Name, fmt.Sprintf("notes#%d", i), "skipped", o.Response.Model, fo, reason); err != nil {
				return nil, err
			}
			res.Decisions = append(res.Decisions, NoteDecision{Notes: fo.SignalRefs, Title: fo.Title, Category: fo.Category, Severity: fo.Severity, Decision: "skipped", Reason: reason})
			continue
		}
		findings = append(findings, f)
	}
	return findings, runErr
}

// collectDecisions lê de run_items o que o Judge (ou a deduplicação) decidiu para cada achado.
func (r *Runner) collectDecisions(ctx context.Context, runID int64, findings []signals.Finding, ns []notes.Note, res *NotesResult) error {
	items, err := r.Store.Q.ListRunItems(ctx, runID)
	if err != nil {
		return err
	}
	byKey := map[string]db.RunItem{}
	for _, it := range items {
		if it.Agent == "judge" {
			byKey[it.ItemKey] = it
		}
	}
	text := map[string]string{}
	for _, n := range ns {
		text[n.ID] = n.Text
	}
	for _, f := range findings {
		d := NoteDecision{Title: f.Title, Fingerprint: f.Fingerprint, Category: f.Category, Severity: f.Severity, Team: f.Routing.Team}
		for _, s := range f.Signals {
			if s.Kind == signals.SourceReviewNote {
				d.Notes = append(d.Notes, s.Summary)
			}
		}
		it, ok := byKey[f.Fingerprint]
		switch {
		case !ok:
			d.Decision = "skipped"
		case it.Status != "ok":
			d.Decision, d.Reason = "skipped", it.Reason.String
		default:
			var out struct {
				Decision string `json:"decision"`
				Verdict  struct {
					Confidence float64 `json:"confidence"`
					Reason     string  `json:"reason"`
					Premise    string  `json:"premise"`
					MergeWith  string  `json:"merge_with"`
				} `json:"verdict"`
			}
			_ = json.Unmarshal(it.Output, &out)
			d.Decision, d.Confidence, d.Reason = out.Decision, out.Verdict.Confidence, out.Verdict.Reason
			d.Premise, d.MergeWith = out.Verdict.Premise, out.Verdict.MergeWith
		}
		res.Decisions = append(res.Decisions, d)
	}
	return nil
}

// excerpts lê, do checkout, as linhas em volta de cada arquivo:linha citado pelas notas. Uma
// janela por arquivo, unindo as referências.
func excerpts(dir string, ns []notes.Note) []notes.Excerpt {
	type window struct{ start, end int }
	byFile := map[string]*window{}
	var order []string
	for _, n := range ns {
		for _, ref := range notes.Refs(n.Text) {
			w, ok := byFile[ref.File]
			if !ok {
				w = &window{start: ref.Start, end: ref.End}
				byFile[ref.File] = w
				order = append(order, ref.File)
			}
			w.start, w.end = min(w.start, ref.Start), max(w.end, ref.End)
		}
	}
	var out []notes.Excerpt
	for _, file := range order {
		clean := filepath.ToSlash(filepath.Clean(file))
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(clean))) //nolint:gosec // caminho relativo validado acima
		if err != nil {
			continue
		}
		lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		w := byFile[file]
		from, to := max(1, w.start-excerptRadius), min(len(lines), w.end+excerptRadius)
		if from > len(lines) {
			continue
		}
		out = append(out, notes.Excerpt{File: clean, StartLine: from, Code: strings.Join(lines[from-1:to], "\n")})
	}
	return out
}
