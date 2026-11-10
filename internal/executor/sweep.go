package executor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// SweptWorktree é um worktree removido pela limpeza automática.
type SweptWorktree struct {
	Path, Issue, Reason string
	Bytes               int64
}

// SweepResult resume uma limpeza: o que saiu e o que deu errado (aviso, não falha o ciclo).
type SweepResult struct {
	Removed  []SweptWorktree
	Warnings []string
}

// sweepReason decide se o worktree da execução mais recente de um caminho pode sair (ADR 0037):
// PR mergeado ou fechado sai logo; falha ou cancelamento, depois de retention (negativo: nunca).
// Achado com execução na fila ou rodando segura o caminho, porque o retry pode retomar nele.
func sweepReason(c db.WorktreeSweepCandidatesRow, now time.Time, retention time.Duration) (string, bool) {
	if c.FindingActive {
		return "", false
	}
	switch c.Status {
	case "succeeded":
		if c.PrState == "merged" || c.PrState == "closed" {
			return "PR " + map[string]string{"merged": "mergeado", "closed": "fechado"}[c.PrState], true
		}
	case "failed", "canceled":
		if retention >= 0 && c.EndedAt.Valid && now.Sub(c.EndedAt.Time) > retention {
			return fmt.Sprintf("%s há mais de %s", map[string]string{"failed": "falhou", "canceled": "cancelada"}[c.Status], days(retention)), true
		}
	}
	return "", false
}

func days(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%d dia(s)", d/(24*time.Hour))
	}
	return d.String()
}

// SweepWorktrees remove os worktrees que não servem mais e grava o espaço liberado.
func (w *Worker) SweepWorktrees(ctx context.Context) (SweepResult, error) {
	var res SweepResult
	cands, err := w.Store.Q.WorktreeSweepCandidates(ctx)
	if err != nil {
		return res, err
	}
	root := abs(w.Cfg.Execution.Worktrees)
	now := time.Now()
	for _, c := range cands {
		why, ok := sweepReason(c, now, w.Cfg.Execution.FailedWorktreeRetention)
		if !ok {
			continue
		}
		path := filepath.Clean(c.Worktree)
		if !strings.HasPrefix(path, root+string(filepath.Separator)) {
			res.Warnings = append(res.Warnings, fmt.Sprintf("worktree %s fora de %s: não removido", path, root))
			continue
		}
		size, err := w.removeWorktree(ctx, path)
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("removendo o worktree %s: %v", path, err))
			continue
		}
		if err := w.Store.Q.MarkWorktreeRemoved(ctx, db.MarkWorktreeRemovedParams{ID: c.ID, FreedBytes: size, Worktree: text(c.Worktree)}); err != nil {
			return res, err
		}
		res.Removed = append(res.Removed, SweptWorktree{Path: path, Issue: c.IssueIdentifier, Reason: why, Bytes: size})
		w.log().Info("worktree removido", "execução", c.ID, "issue", c.IssueIdentifier, "caminho", path, "motivo", why, "bytes", size)
	}
	return res, nil
}

// removeWorktree tira o worktree do clone dono (git worktree remove) e devolve o tamanho que ele
// ocupava. Caminho que já não existe conta como removido, com 0 byte.
func (w *Worker) removeWorktree(ctx context.Context, path string) (int64, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	size := DirSize(path)
	common, err := w.Git.Run(ctx, path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		// Não é mais um worktree registrado (clone apagado ou movido): só o diretório.
		return size, os.RemoveAll(path)
	}
	clone := filepath.Dir(strings.TrimSpace(common))
	if _, err := w.Git.Run(ctx, clone, "worktree", "remove", "--force", path); err != nil {
		return 0, err
	}
	_, _ = w.Git.Run(ctx, clone, "worktree", "prune")
	return size, nil
}

// DirSize soma o tamanho dos arquivos comuns sob dir (sem seguir links).
func DirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil //nolint:nilerr // diretório ilegível não interrompe a soma
		}
		if info, err := d.Info(); err == nil {
			n += info.Size()
		}
		return nil
	})
	return n
}
