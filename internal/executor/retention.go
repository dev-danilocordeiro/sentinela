package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// RetentionResult é o que a retenção removeu num ciclo (D-12 da revisão de prontidão).
type RetentionResult struct {
	JobDirs         int   // diretórios de job removidos
	Bytes           int64 // bytes liberados por eles
	WebhookPayloads int64 // eventos com o corpo esvaziado
	Warnings        []string
}

// jobDirReason diz se o diretório de artefatos do job já pode sair: depois do desfecho (PR
// mergeado ou fechado, falha ou cancelamento) e da retenção, e nunca com o achado na fila ou
// rodando, porque o retry retoma a sessão pela pasta do job (ADR 0030).
func jobDirReason(c db.JobDirSweepCandidatesRow, now time.Time, retention time.Duration) (string, bool) {
	if retention < 0 || c.FindingActive || !c.OutcomeAt.Valid || now.Sub(c.OutcomeAt.Time) <= retention {
		return "", false
	}
	switch c.Status {
	case "succeeded":
		if c.PrState == "merged" || c.PrState == "closed" {
			return fmt.Sprintf("PR %s há mais de %s", map[string]string{"merged": "mergeado", "closed": "fechado"}[c.PrState], days(retention)), true
		}
	case "failed", "canceled":
		return fmt.Sprintf("%s há mais de %s", map[string]string{"failed": "falhou", "canceled": "cancelada"}[c.Status], days(retention)), true
	}
	return "", false
}

// SweepRetention aplica execution.jobs_retention aos diretórios de job e
// execution.webhook_events_retention ao corpo dos webhooks processados. Roda na limpeza do
// worker, a cada ciclo, e no `sentinela job sweep`.
func (w *Worker) SweepRetention(ctx context.Context) (RetentionResult, error) {
	var res RetentionResult
	now := time.Now()
	if keep := w.Cfg.Execution.JobsRetention; keep >= 0 {
		cands, err := w.Store.Q.JobDirSweepCandidates(ctx)
		if err != nil {
			return res, err
		}
		root := abs(w.Cfg.Execution.Jobs)
		for _, c := range cands {
			why, ok := jobDirReason(c, now, keep)
			if !ok {
				continue
			}
			path := filepath.Clean(c.JobDir)
			if _, err := os.Stat(path); err != nil {
				continue // já removido
			}
			if !strings.HasPrefix(path, root+string(filepath.Separator)) {
				res.Warnings = append(res.Warnings, fmt.Sprintf("diretório de job %s fora de %s: não removido", path, root))
				continue
			}
			size := DirSize(path)
			if err := os.RemoveAll(path); err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("removendo o diretório de job %s: %v", path, err))
				continue
			}
			res.JobDirs++
			res.Bytes += size
			w.log().Info("artefatos do job removidos", "execução", c.ID, "caminho", path, "motivo", why, "bytes", size)
		}
	}
	if keep := w.Cfg.Execution.WebhookEventsRetention; keep >= 0 {
		n, err := w.Store.Q.PurgeWebhookPayloads(ctx, pgtype.Timestamptz{Time: now.Add(-keep), Valid: true})
		if err != nil {
			return res, err
		}
		res.WebhookPayloads = n
		if n > 0 {
			w.log().Info("corpo de webhooks processados esvaziado", "eventos", n, "antes de", now.Add(-keep).Format(time.RFC3339))
		}
	}
	return res, nil
}
