package executor

import (
	"context"
	"testing"
	"time"
)

// M5 (revisão 2026-10-11): o desfecho do merge grava quem mergeou, e o do fechamento, quem fechou
// (a decisão é de uma pessoa), não o ator literal "github".
func TestPROutcomeRecordsWhoMergedOrClosed(t *testing.T) {
	for _, c := range []struct {
		name, kind, want string
		set              func(e *env)
	}{
		{"merge", "merged", "ana-gh", func(e *env) {
			now := time.Now()
			e.gh.prs[0].State, e.gh.prs[0].Merged, e.gh.prs[0].MergedAt, e.gh.prs[0].MergeCommitSHA = "closed", true, &now, "abc1234def5678"
			e.gh.prs[0].MergedBy = &struct {
				Login string `json:"login"`
			}{Login: "ana-gh"}
		}},
		{"fechado sem merge", "closed", "joao-gh", func(e *env) {
			e.gh.prs[0].State = "closed"
			e.gh.closedBy = map[int]string{e.gh.prs[0].Number: "joao-gh"}
		}},
		{"fechado sem o evento", "closed", "github", func(e *env) { e.gh.prs[0].State = "closed" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t, true)
			ctx := context.Background()
			e.lin.AddLabel("EX-1", "sentinela-fix")
			_, _ = e.w.Enqueue(ctx)
			job := e.runOnce(t)
			if !job.PrNumber.Valid {
				t.Fatalf("o job deveria abrir PR: %s %s", job.Status, job.Error.String)
			}
			e.gh.mu.Lock()
			c.set(e)
			e.gh.mu.Unlock()
			if _, err := e.w.FollowUp(ctx); err != nil {
				t.Fatal(err)
			}
			var actor string
			if err := e.st.Pool.QueryRow(ctx, "SELECT actor FROM outcomes WHERE finding_id = $1 AND kind = $2", job.FindingID, c.kind).Scan(&actor); err != nil {
				t.Fatal(err)
			}
			if actor != c.want {
				t.Errorf("ator do desfecho %s = %q, quer %q", c.kind, actor, c.want)
			}
		})
	}
}
