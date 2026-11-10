package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

func TestSweepReason(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	ended := func(ago time.Duration) pgtype.Timestamptz {
		return pgtype.Timestamptz{Time: now.Add(-ago), Valid: true}
	}
	week := 7 * 24 * time.Hour
	for _, tc := range []struct {
		name      string
		c         db.WorktreeSweepCandidatesRow
		retention time.Duration
		remove    bool
	}{
		{"PR mergeado", db.WorktreeSweepCandidatesRow{Status: "succeeded", PrState: "merged", EndedAt: ended(time.Minute)}, week, true},
		{"PR fechado", db.WorktreeSweepCandidatesRow{Status: "succeeded", PrState: "closed", EndedAt: ended(time.Minute)}, week, true},
		{"PR aberto", db.WorktreeSweepCandidatesRow{Status: "succeeded", PrState: "open", EndedAt: ended(30 * 24 * time.Hour)}, week, false},
		{"sucesso sem PR (escalado ou bloqueado)", db.WorktreeSweepCandidatesRow{Status: "succeeded", EndedAt: ended(30 * 24 * time.Hour)}, week, false},
		{"falha recente", db.WorktreeSweepCandidatesRow{Status: "failed", EndedAt: ended(6 * 24 * time.Hour)}, week, false},
		{"falha antiga", db.WorktreeSweepCandidatesRow{Status: "failed", EndedAt: ended(8 * 24 * time.Hour)}, week, true},
		{"cancelada antiga", db.WorktreeSweepCandidatesRow{Status: "canceled", EndedAt: ended(8 * 24 * time.Hour)}, week, true},
		{"falha antiga com retenção desligada", db.WorktreeSweepCandidatesRow{Status: "failed", EndedAt: ended(80 * 24 * time.Hour)}, -1, false},
		{"merge com retry na fila", db.WorktreeSweepCandidatesRow{Status: "succeeded", PrState: "merged", FindingActive: true}, week, false},
		{"falha antiga com retry na fila", db.WorktreeSweepCandidatesRow{Status: "failed", EndedAt: ended(80 * 24 * time.Hour), FindingActive: true}, week, false},
		{"rodando", db.WorktreeSweepCandidatesRow{Status: "running", EndedAt: ended(80 * 24 * time.Hour), FindingActive: true}, week, false},
	} {
		if _, got := sweepReason(tc.c, now, tc.retention); got != tc.remove {
			t.Errorf("%s: remove = %v, quer %v", tc.name, got, tc.remove)
		}
	}
}

// O worktree do job com PR fica enquanto o PR está aberto e sai quando ele é mergeado; o de uma
// falha antiga sai, o de uma falha recente fica, e caminho fora da raiz nunca é tocado.
func TestSweepRemovesWorktreesAfterMergeAndOldFailures(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	e.lin.AddLabel("EX-1", "sentinela-fix")
	if _, err := e.w.Enqueue(ctx); err != nil {
		t.Fatal(err)
	}
	job := e.runOnce(t)
	wt := job.Worktree.String
	if !job.PrNumber.Valid || !isDir(wt) {
		t.Fatalf("job sem PR ou sem worktree: %+v", job)
	}
	if res, err := e.w.SweepWorktrees(ctx); err != nil || len(res.Removed) != 0 {
		t.Fatalf("PR aberto: removidos %+v, %v", res.Removed, err)
	}

	if _, err := e.st.Pool.Exec(ctx, "UPDATE executions SET pr_state = 'merged' WHERE id = $1", job.ID); err != nil {
		t.Fatal(err)
	}
	// Falhas de outro achado: uma antiga e uma recente, em diretórios simples sob a raiz, e uma
	// antiga fora da raiz.
	old := filepath.Join(e.cfg.Execution.Worktrees, "acme", "svc", "EX-9.exec-7")
	recent := filepath.Join(e.cfg.Execution.Worktrees, "acme", "svc", "EX-8")
	outside := filepath.Join(t.TempDir(), "EX-7")
	for _, d := range []string{old, recent, outside} {
		write(d, "arquivo.txt", strings.Repeat("x", 2048))
	}
	addFinding(t, e, "bbbbbbbbbbbb", "Outro achado", []string{"cmd/x"}, "cmd/x/x.go")
	var other int64
	if err := e.st.Pool.QueryRow(ctx, "SELECT id FROM findings WHERE fingerprint = 'bbbbbbbbbbbb'").Scan(&other); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct {
		path string
		ago  string
	}{{old, "8 days"}, {recent, "1 day"}, {outside, "30 days"}} {
		if _, err := e.st.Pool.Exec(ctx, `INSERT INTO executions (finding_id, status, repo, issue_identifier, worktree, finished_at)
			VALUES ($1, 'failed', 'acme/svc', 'EX-9', $2, now() - $3::interval)`, other, r.path, r.ago); err != nil {
			t.Fatal(err)
		}
	}

	res, err := e.w.SweepWorktrees(ctx)
	if err != nil {
		t.Fatal(err)
	}
	removed := map[string]int64{}
	for _, r := range res.Removed {
		removed[r.Path] = r.Bytes
	}
	if len(removed) != 2 || removed[wt] == 0 || removed[old] != 2048 {
		t.Errorf("removidos = %+v", res.Removed)
	}
	if isDir(wt) || isDir(old) || !isDir(recent) || !isDir(outside) {
		t.Errorf("no disco: merge %v, falha antiga %v, recente %v, fora %v", isDir(wt), isDir(old), isDir(recent), isDir(outside))
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "fora de") {
		t.Errorf("avisos = %v", res.Warnings)
	}
	clone := filepath.Join(e.w.Git.Root, "acme", "svc")
	if list := gitIn(t, clone, "worktree", "list"); strings.Contains(list, wt) {
		t.Errorf("worktree ainda registrado no clone:\n%s", list)
	}
	tot, err := e.st.Q.WorktreeCleanupTotals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tot.Removed != 2 || tot.FreedBytes != removed[wt]+2048 || tot.FreedWeek != tot.FreedBytes {
		t.Errorf("totais = %+v", tot)
	}
	// Uma segunda limpeza não remove nem conta de novo.
	if res, err := e.w.SweepWorktrees(ctx); err != nil || len(res.Removed) != 0 {
		t.Errorf("segunda limpeza: %+v, %v", res.Removed, err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Error(err)
	}
}
