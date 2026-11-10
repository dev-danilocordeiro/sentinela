package executor

import (
	"context"
	"sync"
	"testing"
	"time"
)

// M3 (revisão 2026-10-11): dois acompanhamentos ao mesmo tempo (serve pelo webhook e worker)
// gravam um desfecho só para o mesmo PR mergeado.
func TestConcurrentFollowUpsRecordASingleOutcome(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	e.lin.AddLabel("EX-1", "sentinela-fix")
	_, _ = e.w.Enqueue(ctx)
	job := e.runOnce(t)
	if !job.PrNumber.Valid {
		t.Fatalf("o job deveria abrir PR: %s %s", job.Status, job.Error.String)
	}
	execs, err := e.st.Q.OpenPRExecutions(ctx)
	if err != nil || len(execs) != 1 {
		t.Fatalf("execuções com PR aberto: %d, %v", len(execs), err)
	}
	e.gh.mu.Lock()
	now := time.Now()
	e.gh.prs[0].State, e.gh.prs[0].Merged, e.gh.prs[0].MergedAt, e.gh.prs[0].MergeCommitSHA = "closed", true, &now, "abc1234def5678"
	e.gh.mu.Unlock()
	// Os dois leram o PR aberto antes de qualquer um gravar: o mesmo db.Execution nos dois.
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = e.w.followOne(ctx, execs[0])
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := e.st.Pool.QueryRow(ctx, "SELECT count(*) FROM outcomes WHERE finding_id = $1 AND kind = 'merged'", job.FindingID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("desfechos merged = %d, quer 1", n)
	}
}
