package executor

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/github"
	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/local"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// Modo local (ADR 0038): sem Linear, o pedido vem de `finding fix`, o job roda com a spec montada
// do próprio achado e abre o PR em draft sem "Fixes"; nada vai ao Linear. Com gh_cli, a review do
// gate só comenta, mesmo com o gate aprovando e a trava da config liberada.
func TestLocalModeRunsWithoutLinearAndGHCLIOnlyComments(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	e.cfg.Adapters.Tracker, e.cfg.Adapters.GitHost = config.TrackerLocal, config.GitHostGHCLI
	e.cfg.Teams["core"].Policy.AutoApprove = ptr(true)
	e.cfg.GitHubApps.ReviewerApproves = true // o Validate recusa; aqui a trava do worker é que segura
	fid := addFindingLocal(t, e)
	e.w.Linear = nil
	opsBefore := len(e.lin.Ops)
	if q, err := e.w.Enqueue(ctx); err != nil || len(q) != 0 {
		t.Fatalf("sem Linear o Enqueue não enfileira nada: %v, %v", q, err)
	}
	tr := &local.Tracker{Cfg: e.cfg, Store: e.st}
	queued, err := tr.Fix(ctx, fid, local.Decision{Actor: "dono"})
	if err != nil {
		t.Fatal(err)
	}
	e.gh.mu.Lock()
	e.gh.checks = map[string][]github.CheckRun{}
	e.gh.mu.Unlock()
	job := e.runOnce(t)
	if job.ID != queued.ID || job.Status != "succeeded" || !job.PrNumber.Valid {
		t.Fatalf("job = %+v", job)
	}
	issue := "F-" + strconv.FormatInt(fid, 10)
	if job.Branch.String != "sentinela/"+issue {
		t.Errorf("branch = %s", job.Branch.String)
	}
	prs, _ := e.gh.snapshot()
	pr := prs[len(prs)-1]
	if !pr.Draft || strings.Contains(pr.Body, "Fixes") || !strings.HasPrefix(pr.Body, "Achado "+issue+" do Sentinela") || !strings.HasPrefix(pr.Title, issue+": ") {
		t.Errorf("PR: draft %v, título %q, corpo:\n%s", pr.Draft, pr.Title, pr.Body)
	}
	head := job.HeadSha.String
	e.gh.mu.Lock()
	e.gh.checks[head] = []github.CheckRun{{ID: 1, Name: "ci", Status: "completed", Conclusion: "success"}}
	e.gh.prs[len(e.gh.prs)-1].Head.SHA = head
	e.gh.mu.Unlock()
	if _, err := e.w.FollowUp(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := e.st.Q.GetExecution(ctx, job.ID); err != nil || got.Gate.String != "approve" {
		t.Fatalf("o gate deveria aprovar (para a trava do gh_cli valer algo): %q, %v", got.Gate.String, err)
	}
	_, reviews := e.gh.snapshot()
	for _, r := range reviews {
		if r.Event != "COMMENT" {
			t.Errorf("review %s pela conta do gh: %s", r.Event, r.Body)
		}
	}
	if len(reviews) == 0 {
		t.Error("o gate não comentou")
	}
	if ops := e.lin.Ops[opsBefore:]; len(ops) != 0 {
		t.Errorf("chamadas ao Linear no modo local: %v", ops)
	}
}

func addFindingLocal(t *testing.T, e *env) int64 {
	t.Helper()
	addFinding(t, e, "bbbbbbbbbbbb", "Fecha o body da resposta", []string{"cmd/api"}, "cmd/api/main.go")
	var id int64
	if err := e.st.Pool.QueryRow(context.Background(), "SELECT id FROM findings WHERE fingerprint = 'bbbbbbbbbbbb'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	// Sem issue: o achado só existe no Postgres, como no tracker local.
	if _, err := e.st.Pool.Exec(context.Background(), "DELETE FROM linear_issues WHERE finding_id = $1", id); err != nil {
		t.Fatal(err)
	}
	return id
}

// teams.<time>.labels troca o gatilho e o resultado (ADR 0038): a label da organização não
// dispara mais o time que trocou, e a dele dispara.
func TestTeamTriggerAndFailedLabelsOverrideTheOrganization(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	e.lin.Labels["corrigir"], e.lin.Labels["falhou"] = "lbl-corrigir", "lbl-falhou"
	e.cfg.Teams["core"].Labels = config.TeamLabels{Trigger: "corrigir", Failed: "falhou"}
	e.lin.AddLabel("EX-1", "sentinela-fix")
	if q, err := e.w.Enqueue(ctx); err != nil || len(q) != 0 {
		t.Fatalf("a label da organização não é o gatilho do core: %v, %v", q, err)
	}
	e.lin.AddLabel("EX-1", "corrigir")
	q, err := e.w.Enqueue(ctx)
	if err != nil || len(q) != 1 {
		t.Fatalf("a label do time deveria enfileirar: %v, %v", q, err)
	}
	e.runner.breakAt = "decision.md"
	job := e.runOnce(t)
	if job.Status != "failed" {
		t.Fatalf("job = %s", job.Status)
	}
	added := e.lin.Snapshot()[0].LabelsAddedBySentinela
	if !slicesContains(added, "lbl-falhou") || slicesContains(added, "lbl-failed") {
		t.Errorf("labels aplicadas = %v (quer a de falha do time)", added)
	}
	if labels := issueLabels(e, "EX-1"); labels["lbl-corrigir"] {
		t.Errorf("o gatilho do time deveria sair depois da falha sem PR: %v", labels)
	}
}

func issueLabels(e *env, identifier string) map[string]bool {
	out := map[string]bool{}
	for _, is := range e.lin.Issues {
		if is.Identifier == identifier {
			for _, id := range is.LabelIDs {
				out[id] = true
			}
		}
	}
	return out
}

// staleTracker guarda um estado velho (Todo) e, lido na hora, a issue já está cancelada.
type staleTracker struct {
	linear.Tracker
	moved []string
}

func (s *staleTracker) TeamRefs(_ context.Context, key string) (*linear.TeamRefs, error) {
	return &linear.TeamRefs{ID: key, Key: key, States: map[string]string{"In Review": "In Review", "Done": "Done"}}, nil
}

func (s *staleTracker) Issues(_ context.Context, ids []string) ([]linear.Issue, error) {
	is := linear.Issue{ID: ids[0]}
	is.State.Name, is.State.Type = "Todo", "unstarted"
	return []linear.Issue{is}, nil
}

func (s *staleTracker) IssuesFresh(_ context.Context, ids []string) ([]linear.Issue, error) {
	is := linear.Issue{ID: ids[0]}
	is.State.Name, is.State.Type = "Canceled", "canceled"
	return []linear.Issue{is}, nil
}

func (s *staleTracker) SetState(_ context.Context, id, state string) error {
	s.moved = append(s.moved, id+"->"+state)
	return nil
}

// Com estado guardado (linear_mcp), o worker lê a issue na hora antes de mover: uma issue que uma
// pessoa acabou de cancelar não volta para In Review.
func TestMoveIssueReadsFreshStateBeforeChangingIt(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	st := &staleTracker{}
	e.w.Linear = st
	e.cfg.Defaults.Linear.ReviewState = "In Review"
	var fid int64
	var lid string
	if err := e.st.Pool.QueryRow(ctx, "SELECT finding_id, linear_id FROM linear_issues LIMIT 1").Scan(&fid, &lid); err != nil {
		t.Fatal(err)
	}
	e.w.moveIssue(ctx, db.Execution{FindingID: fid, LinearIssueID: lid, IssueIdentifier: "EX-1"}, "In Review", "revisão")
	if len(st.moved) != 0 {
		t.Errorf("moveu uma issue cancelada: %v", st.moved)
	}
}
