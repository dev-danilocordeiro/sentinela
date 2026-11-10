package executor

import (
	"context"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/linear"
)

func closeFakePR(e *env) {
	e.gh.mu.Lock()
	defer e.gh.mu.Unlock()
	e.gh.prs[0].State = "closed"
}

// C3 (revisão 2026-10-11): a pessoa cancela a issue com o PR aberto (recusa, com supressão) e
// depois fecha o PR sem merge. O fechamento grava o outcome closed, mas não devolve o achado para
// open nem cria outra supressão: a decisão humana fica.
func TestClosingThePRAfterARejectionKeepsTheHumanDecision(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	e.lin.AddLabel("EX-1", "sentinela-fix")
	_, _ = e.w.Enqueue(ctx)
	job := e.runOnce(t)
	if !job.PrNumber.Valid {
		t.Fatalf("o job deveria abrir PR: %s %s", job.Status, job.Error.String)
	}
	e.lin.Cancel("EX-1", "Ana", "Não vamos mexer nisso agora.")
	syncer := &linear.Syncer{Store: e.st, Linear: e.w.Linear, Cfg: e.cfg}
	if _, err := syncer.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	closeFakePR(e)
	if _, err := e.w.FollowUp(ctx); err != nil {
		t.Fatal(err)
	}
	e.lin.Comment("EX-1", "Ana", "Mais um comentário depois de fechar.") // a issue muda de novo
	if _, err := syncer.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	var rejected, closed, sups int
	_ = e.st.Pool.QueryRow(ctx, "SELECT state FROM findings WHERE id = $1", job.FindingID).Scan(&state)
	_ = e.st.Pool.QueryRow(ctx, "SELECT count(*) FILTER (WHERE kind = 'rejected'), count(*) FILTER (WHERE kind = 'closed') FROM outcomes WHERE finding_id = $1", job.FindingID).Scan(&rejected, &closed)
	_ = e.st.Pool.QueryRow(ctx, "SELECT count(*) FROM suppressions WHERE finding_id = $1", job.FindingID).Scan(&sups)
	if state != "rejected" || rejected != 1 || closed != 1 || sups != 1 {
		t.Errorf("achado %s; outcomes rejected %d, closed %d; supressões %d (quer rejected, 1, 1, 1)", state, rejected, closed, sups)
	}
}

// Sem decisão humana e com a issue aberta, fechar o PR devolve o achado para open (o time pode
// pedir a correção de novo).
func TestClosingThePRWithoutADecisionReopensTheFinding(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	e.lin.AddLabel("EX-1", "sentinela-fix")
	_, _ = e.w.Enqueue(ctx)
	job := e.runOnce(t)
	closeFakePR(e)
	if _, err := e.w.FollowUp(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	var closed int
	_ = e.st.Pool.QueryRow(ctx, "SELECT state FROM findings WHERE id = $1", job.FindingID).Scan(&state)
	_ = e.st.Pool.QueryRow(ctx, "SELECT count(*) FROM outcomes WHERE finding_id = $1 AND kind = 'closed'", job.FindingID).Scan(&closed)
	if state != "open" || closed != 1 {
		t.Errorf("achado %s, outcomes closed %d (quer open, 1)", state, closed)
	}
}
