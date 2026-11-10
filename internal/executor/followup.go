package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dev-danilocordeiro/sentinela/internal/gate"
	"github.com/dev-danilocordeiro/sentinela/internal/github"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// handled é um evento já tratado para um PR (gravado em executions.shepherd_handled).
type handled struct {
	Kind string `json:"kind"`          // ci | review | gate | merged | closed | exhausted
	Ref  string `json:"ref"`           // sha do head ou id da review
	At   string `json:"at,omitempty"`  // merged_at (merged)
	SHA  string `json:"sha,omitempty"` // merge commit (merged)
}

func handledSet(raw []byte) map[string]bool {
	var hs []handled
	_ = json.Unmarshal(raw, &hs)
	out := map[string]bool{}
	for _, h := range hs {
		out[h.Kind+":"+h.Ref] = true
	}
	return out
}

// FollowUpResult resume um ciclo de acompanhamento.
type FollowUpResult struct {
	Checked  int
	Events   []string
	Warnings []string
}

// FollowUp acompanha os PRs abertos pelo Sentinela: desfecho (merge, fechamento), CI e reviews
// humanas. CI vermelho ou review pedindo mudança disparam o PR Shepherd (no máximo
// execution.shepherd_max_cycles ciclos por PR). Com o CI verde, o gate é reavaliado e a revisora
// comenta de novo. Nada aqui aprova ou faz merge.
func (w *Worker) FollowUp(ctx context.Context) (FollowUpResult, error) {
	var res FollowUpResult
	execs, err := w.Store.Q.OpenPRExecutions(ctx)
	if err != nil {
		return res, err
	}
	if len(execs) > 0 && w.Bot == nil {
		return res, fmt.Errorf("há PRs abertos mas a app bot não está configurada")
	}
	for _, e := range execs {
		res.Checked++
		evs, err := w.followOne(ctx, e)
		res.Events = append(res.Events, evs...)
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s #%d: %v", e.Repo, e.PrNumber.Int32, err))
		}
	}
	reverted, err := w.detectReverts(ctx)
	res.Events = append(res.Events, reverted...)
	if err != nil {
		res.Warnings = append(res.Warnings, "reverts: "+err.Error())
	}
	return res, nil
}

func (w *Worker) followOne(ctx context.Context, e db.Execution) ([]string, error) {
	gh, err := w.client(ctx, w.Bot)
	if err != nil {
		return nil, err
	}
	n := int(e.PrNumber.Int32)
	pr, err := gh.GetPR(ctx, e.Repo, n)
	if err != nil {
		return nil, err
	}
	done := handledSet(e.ShepherdHandled)
	tag := fmt.Sprintf("%s#%d", e.Repo, n)
	switch {
	case pr.Merged:
		at := ""
		if pr.MergedAt != nil {
			at = pr.MergedAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		actor := ""
		if pr.MergedBy != nil {
			actor = pr.MergedBy.Login
		}
		if err := w.closePR(ctx, e, "merged", "fixed", actor, handled{Kind: "merged", Ref: pr.MergeCommitSHA, At: at, SHA: pr.MergeCommitSHA}); err != nil {
			return nil, err
		}
		// Depois do desfecho gravado: o sync do Linear vê a issue concluída com o achado já fixed
		// e não grava um segundo desfecho.
		w.moveToDone(ctx, e)
		return []string{tag + " mergeado"}, nil
	case pr.State == "closed":
		// Quem fechou vem do evento closed; sem ele (erro na API), o desfecho sai com o ator "github".
		actor, _ := gh.ClosedBy(ctx, e.Repo, n)
		return []string{tag + " fechado sem merge"}, w.closePR(ctx, e, "closed", "open", actor, handled{Kind: "closed", Ref: pr.Head.SHA})
	}
	var events []string
	sha := pr.Head.SHA
	runs, err := gh.CheckRuns(ctx, e.Repo, sha)
	if err != nil {
		return nil, err
	}
	ci := ciFrom(runs)
	switch {
	case ci == gate.CIFailure && !done["ci:"+sha]:
		var b strings.Builder
		fmt.Fprintf(&b, "# CI vermelho no PR %s, commit %s\n\n", tag, sha)
		for _, r := range runs {
			if r.Status != "completed" || r.Conclusion == "success" || r.Conclusion == "skipped" || r.Conclusion == "neutral" {
				continue
			}
			fmt.Fprintf(&b, "## %s: %s\n\n%s\n\n%s\n\n", r.Name, r.Conclusion, r.Output.Title, truncate(r.Output.Summary, 2000))
			if anns, err := gh.Annotations(ctx, e.Repo, r.ID); err == nil {
				for _, a := range anns {
					fmt.Fprintf(&b, "- `%s:%d` (%s): %s\n", a.Path, a.StartLine, a.Level, truncate(a.Message, 300))
				}
			}
			fmt.Fprintf(&b, "\nDetalhes: %s\n\n", r.HTMLURL)
		}
		ev, err := w.shepherd(ctx, e, handled{Kind: "ci", Ref: sha}, b.String())
		return append(events, ev...), err
	case (ci == gate.CISuccess || ci == gate.CINone) && !done["gate:"+sha]:
		g, err := w.regate(ctx, e, sha)
		if err != nil {
			return events, err
		}
		if err := w.Store.Q.MarkShepherdHandled(ctx, db.MarkShepherdHandledParams{ID: e.ID, Event: mustJSON([]handled{{Kind: "gate", Ref: sha}})}); err != nil {
			return events, err
		}
		events = append(events, fmt.Sprintf("%s CI %s: gate %s (revisora comentou)", tag, ci, g.Decision))
	}
	reviews, err := gh.Reviews(ctx, e.Repo, n)
	if err != nil {
		return events, err
	}
	for _, r := range reviews {
		ref := strconv.FormatInt(r.ID, 10)
		if r.State != "CHANGES_REQUESTED" || r.User.Type == "Bot" || done["review:"+ref] {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "# Review de %s pedindo mudança no PR %s\n\n%s\n\n", r.User.Login, tag, r.Body)
		if comments, err := gh.ReviewComments(ctx, e.Repo, n); err == nil {
			for _, c := range comments {
				if c.PullRequestReviewID == r.ID {
					fmt.Fprintf(&b, "- `%s:%d`: %s\n", c.Path, c.Line, c.Body)
				}
			}
		}
		ev, err := w.shepherd(ctx, e, handled{Kind: "review", Ref: ref}, b.String())
		events = append(events, ev...)
		if err != nil {
			return events, err
		}
		break // um ciclo por vez; o próximo FollowUp vê o resto
	}
	return events, nil
}

// closePR grava o desfecho do PR e devolve o achado ao estado certo, numa transação (um erro no
// meio não deixa o desfecho sem o estado). Merge: o achado vira fixed. Fechado sem merge: o
// outcome closed fica gravado, mas o achado só volta a open se nenhuma decisão humana o resolveu
// (ReopenAfterClosedPR); nunca desfaz uma recusa nem cria outra supressão.
func (w *Worker) closePR(ctx context.Context, e db.Execution, prState, findingState, actor string, h handled) error {
	if actor == "" {
		actor = "github"
	}
	return pgx.BeginFunc(ctx, w.Store.Pool, func(tx pgx.Tx) error {
		q := w.Store.Q.WithTx(tx)
		n, err := q.SetPRState(ctx, db.SetPRStateParams{Repo: e.Repo, PrState: text(prState), PrNumber: e.PrNumber})
		if err != nil {
			return err
		}
		if n == 0 {
			return nil // outro processo já gravou o desfecho deste PR (M3 da revisão de 2026-10-11)
		}
		if err := q.InsertExecutionOutcome(ctx, db.InsertExecutionOutcomeParams{
			FindingID: e.FindingID, ExecutionID: pgtype.Int8{Int64: e.ID, Valid: true}, Kind: prState,
			Reason: text("PR " + e.PrUrl.String), Actor: text(actor),
		}); err != nil {
			return err
		}
		if findingState == "open" {
			if _, err := q.ReopenAfterClosedPR(ctx, e.FindingID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		} else {
			if err := q.SetFindingState(ctx, db.SetFindingStateParams{ID: e.FindingID, State: findingState}); err != nil {
				return err
			}
		}
		if err := q.PropagateMergedState(ctx, e.FindingID); err != nil {
			return err
		}
		// Por último: um erro antes deixa o evento para o próximo ciclo.
		return q.MarkShepherdHandled(ctx, db.MarkShepherdHandledParams{ID: e.ID, Event: mustJSON([]handled{h})})
	})
}

// detectReverts procura, na branch padrão, um commit "This reverts commit <merge>" de PRs
// mergeados nos últimos 30 dias.
func (w *Worker) detectReverts(ctx context.Context) ([]string, error) {
	execs, err := w.Store.Q.MergedWithoutRevert(ctx)
	if err != nil || len(execs) == 0 {
		return nil, err
	}
	gh, err := w.client(ctx, w.Bot)
	if err != nil {
		return nil, err
	}
	var events []string
	for _, e := range execs {
		var hs []handled
		_ = json.Unmarshal(e.ShepherdHandled, &hs)
		var merged *handled
		for i := range hs {
			if hs[i].Kind == "merged" && hs[i].SHA != "" {
				merged = &hs[i]
			}
		}
		rc, ok := w.Cfg.Repo(e.Repo)
		if merged == nil || !ok {
			continue
		}
		since, err := parseTime(merged.At)
		if err != nil {
			continue
		}
		commits, err := gh.CommitsSince(ctx, e.Repo, rc.DefaultBranch, since)
		if err != nil {
			return events, err
		}
		for _, c := range commits {
			if !strings.Contains(c.Commit.Message, "This reverts commit "+merged.SHA) {
				continue
			}
			if err := w.Store.Q.InsertExecutionOutcome(ctx, db.InsertExecutionOutcomeParams{
				FindingID: e.FindingID, ExecutionID: pgtype.Int8{Int64: e.ID, Valid: true}, Kind: "reverted",
				Reason: text("revert " + c.SHA), Actor: text("github"),
			}); err != nil {
				return events, err
			}
			if err := w.setFindingState(ctx, e.FindingID, "reverted"); err != nil {
				return events, err
			}
			events = append(events, fmt.Sprintf("%s#%d revertido em %s", e.Repo, e.PrNumber.Int32, c.SHA[:7]))
			break
		}
	}
	return events, nil
}

// regate reavalia o gate com o CI atual e a revisora comenta de novo.
func (w *Worker) regate(ctx context.Context, e db.Execution, sha string) (*gate.Result, error) {
	f, err := w.finding(ctx, e.FindingID)
	if err != nil {
		return nil, err
	}
	rc, ok := w.Cfg.Repo(e.Repo)
	if !ok {
		return nil, fmt.Errorf("repo %s fora do sentinela.yaml", e.Repo)
	}
	ws := &workspace{rc: rc, worktree: e.Worktree.String, branch: e.Branch.String}
	if ws.base, err = w.Git.Run(ctx, ws.worktree, "merge-base", "origin/"+rc.DefaultBranch, "HEAD"); err != nil {
		return nil, err
	}
	pr := &github.PullRequest{Number: int(e.PrNumber.Int32)}
	pr.Head.SHA = sha
	approver := map[string]string{"approve": "APPROVE", "escalate": "ESCALATE"}[e.Decision.String]
	return w.gateAndReview(ctx, e, f, ws, pr, approver, e.DecisionReason.String, e.ApprovedBy.String)
}

// shepherd roda um ciclo do PR Shepherd: o Claude Code headless corrige no worktree e o worker
// faz push. Ciclos esgotados viram aviso, uma vez.
func (w *Worker) shepherd(ctx context.Context, e db.Execution, h handled, event string) ([]string, error) {
	tag := fmt.Sprintf("%s#%d", e.Repo, e.PrNumber.Int32)
	if int(e.ShepherdCycles) >= w.Cfg.Execution.ShepherdMaxCycles {
		if handledSet(e.ShepherdHandled)["exhausted:"+h.Kind] {
			return nil, nil
		}
		_ = w.Store.Q.MarkShepherdHandled(ctx, db.MarkShepherdHandledParams{ID: e.ID, Event: mustJSON([]handled{h, {Kind: "exhausted", Ref: h.Kind}})})
		msg := fmt.Sprintf("**Sentinela · PR Shepherd** · %s: esgotou os %d ciclos (%s); o PR precisa de um humano.", e.IssueIdentifier, w.Cfg.Execution.ShepherdMaxCycles, h.Kind)
		if e.LinearIssueID != "" {
			_ = w.comment(ctx, e.LinearIssueID, msg)
		}
		return []string{tag + ": shepherd esgotado"}, nil
	}
	// Cota da assinatura esgotada: o evento fica para depois do reset, sem gastar ciclo.
	if until, err := w.QuotaPausedUntil(ctx); err != nil || !until.IsZero() {
		return nil, err
	}
	rc, ok := w.Cfg.Repo(e.Repo)
	if !ok {
		return nil, fmt.Errorf("repo %s fora do sentinela.yaml", e.Repo)
	}
	co, err := w.Git.Sync(ctx, rc.Name, rc.DefaultBranch)
	if err != nil {
		return nil, err
	}
	branch := e.Branch.String
	if err := w.Git.FetchBranch(ctx, co, branch); err != nil {
		return nil, err
	}
	if err := w.Git.Worktree(ctx, co, e.Worktree.String, branch, "origin/"+branch); err != nil {
		return nil, err
	}
	if _, err := w.Git.Run(ctx, e.Worktree.String, "merge", "--ff-only", "origin/"+branch); err != nil {
		return nil, fmt.Errorf("worktree divergiu do PR: %w", err)
	}
	cycle := int(e.ShepherdCycles) + 1
	cycleDir := filepath.Join(e.JobDir.String, fmt.Sprintf("shepherd-%d", cycle))
	if err := os.MkdirAll(cycleDir, 0o750); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(cycleDir, "event.md"), []byte(event), 0o600); err != nil {
		return nil, err
	}
	before, _ := w.Git.Run(ctx, e.Worktree.String, "rev-parse", "HEAD")
	idle, completion := w.Cfg.Execution.Watchdog()
	run, err := w.Runner.Run(ctx, Invocation{
		Prompt: "/sentinela-factory:sentinela-shepherd " + cycleDir, Dir: e.Worktree.String,
		PluginDir: filepath.Join(e.JobDir.String, "plugin"), JobDir: e.JobDir.String, Model: w.models(ctx, e).Shepherd,
		MaxBudget: w.Cfg.Execution.MaxBudgetUSD, Disallowed: w.Cfg.Execution.DisallowedTools,
		Timeout: w.Cfg.Execution.Timeout, IdleTimeout: idle, CompletionTimeout: completion,
		StreamFile: filepath.Join(cycleDir, "stream.jsonl"),
	})
	w.recordCalls(ctx, e, run, "shepherd")
	if o := (&outcome{run: run, failErr: err}); classifyFailure(err, run) == FailUsageLimit || (run.IsError && llm.UsageLimited(run.Result)) {
		// Limite de uso: não conta ciclo nem comenta; o evento volta depois do reset.
		msg := usageLimitMessage(o)
		reset, ok := llm.UsageResetAt(msg, w.now())
		if !ok || !reset.After(w.now()) {
			reset = w.now().Add(quotaFallback)
		}
		w.pauseForQuota(ctx, reset, msg, "PR Shepherd · "+tag)
		return []string{fmt.Sprintf("%s: shepherd adiado pela cota da assinatura até %s", tag, formatReset(reset))}, nil
	}
	cost := store.NanoUSDToNumeric(int64(run.CostUSD * 1e9))
	record := func(head string) error {
		_, err := w.Store.Q.RecordShepherdCycle(ctx, db.RecordShepherdCycleParams{ID: e.ID, Event: mustJSON([]handled{h}), HeadSha: text(head), CostUsd: cost})
		return err
	}
	if err != nil {
		_ = record("")
		return []string{fmt.Sprintf("%s: shepherd ciclo %d falhou", tag, cycle)}, err
	}
	art, cerr := CollectShepherd(cycleDir)
	after, _ := w.Git.Run(ctx, e.Worktree.String, "rev-parse", "HEAD")
	if cerr != nil || art.Verdict != "STATUS: FIXED" || after == before {
		_ = record("")
		why := "sem commit"
		if cerr != nil {
			why = cerr.Error()
		} else if art.Verdict != "STATUS: FIXED" {
			why = secondLine(art.Summary)
		}
		if e.LinearIssueID != "" {
			_ = w.comment(ctx, e.LinearIssueID, fmt.Sprintf("**Sentinela · PR Shepherd** ciclo %d (%s) não corrigiu: %s", cycle, h.Kind, why))
		}
		return []string{fmt.Sprintf("%s: shepherd ciclo %d bloqueado (%s)", tag, cycle, why)}, nil
	}
	tok, err := w.Bot.Token(ctx)
	if err != nil {
		return nil, err
	}
	pushGit := &repo.Git{Root: w.Git.Root, Token: tok, BaseURL: w.Git.BaseURL}
	if err := pushGit.Push(ctx, e.Worktree.String, branch, rc.DefaultBranch); err != nil {
		_ = record("")
		return nil, err
	}
	if err := record(after); err != nil {
		return nil, err
	}
	if e.LinearIssueID != "" {
		_ = w.comment(ctx, e.LinearIssueID, fmt.Sprintf("**Sentinela · PR Shepherd** ciclo %d/%d (%s): novo commit %s no %s.", cycle, w.Cfg.Execution.ShepherdMaxCycles, h.Kind, after[:7], e.PrUrl.String))
	}
	return []string{fmt.Sprintf("%s: shepherd ciclo %d (%s) empurrou %s", tag, cycle, h.Kind, after[:7])}, nil
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func parseTime(s string) (t time.Time, err error) { return time.Parse("2006-01-02T15:04:05Z", s) }

// setFindingState muda o estado do achado e das duplicatas humanas dele (merged_into).
func (w *Worker) setFindingState(ctx context.Context, id int64, state string) error {
	if err := w.Store.Q.SetFindingState(ctx, db.SetFindingStateParams{ID: id, State: state}); err != nil {
		return err
	}
	return w.Store.Q.PropagateMergedState(ctx, id)
}
