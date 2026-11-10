package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dev-danilocordeiro/sentinela/internal/llm"
	"github.com/dev-danilocordeiro/sentinela/internal/notify"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// Cota da assinatura (bug de 2026-10-14, execuções 24 a 30). Quando o Claude Code termina com
// limite de uso (semanal, de sessão ou rate limit), o job não falhou: ele volta para a fila até o
// reset, com a label de gatilho e sem comentário na issue, e o worker para de pegar jobs até lá.
// Depois do reset, o mesmo job continua a sessão de onde parou.
const (
	// QuotaWaitPrefix começa o executions.wait_reason de um job devolvido pela cota.
	QuotaWaitPrefix = "cota da assinatura até "
	// quotaMargin é a folga depois do reset lido na mensagem.
	quotaMargin = 5 * time.Minute
	// quotaFallback é a pausa quando a mensagem não diz o reset (rate limit da API).
	quotaFallback = 30 * time.Minute
	// quotaKey guarda em executions.artifacts quando e por que o job parou.
	quotaKey = "quota"
)

// quotaState é a pausa conhecida por este processo; a do banco (QuotaPausedUntil) cobre o restart.
type quotaState struct {
	mu    sync.Mutex
	until time.Time
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// QuotaPausedUntil diz até quando o worker não pega jobs por causa da cota (zero: não está
// pausado).
func (w *Worker) QuotaPausedUntil(ctx context.Context) (time.Time, error) {
	w.quota.mu.Lock()
	until := w.quota.until
	w.quota.mu.Unlock()
	fromDB, err := w.Store.Q.QuotaPausedUntil(ctx, QuotaWaitPrefix)
	if err != nil {
		return time.Time{}, err
	}
	if fromDB.Valid && fromDB.Time.After(until) {
		until = fromDB.Time
	}
	if !until.After(w.now()) {
		return time.Time{}, nil
	}
	return until, nil
}

// formatReset escreve o horário do reset como o wait_reason e o aviso mostram.
func formatReset(t time.Time) string {
	s := t.Format("02/01 15:04")
	if name := t.Location().String(); name != "Local" && name != "" {
		s += " (" + name + ")"
	}
	return s
}

// usageLimitMessage é a mensagem do limite: o resultado da sessão ou o erro.
func usageLimitMessage(o *outcome) string {
	if llm.UsageLimited(o.run.Result) {
		return o.run.Result
	}
	if o.failErr != nil {
		return o.failErr.Error()
	}
	return o.run.Result
}

// requeueForQuota devolve à fila o job que parou no limite de uso, guardando a sessão e o estado
// do worktree para continuar, e pausa o worker. false: não deu para devolver (o job segue o
// caminho de falha normal).
func (w *Worker) requeueForQuota(ctx context.Context, e db.Execution, ws *workspace, o *outcome) bool {
	ctx = context.WithoutCancel(ctx)
	msg := usageLimitMessage(o)
	now := w.now()
	reset, ok := llm.UsageResetAt(msg, now)
	if !ok || !reset.After(now) {
		reset = now.Add(quotaFallback)
	}
	all := map[string]Artifact{
		quotaKey: {File: now.UTC().Format(time.RFC3339), Verdict: "QUOTA: " + reset.UTC().Format(time.RFC3339), Summary: truncate(msg, 400)},
	}
	if ws != nil && isDir(ws.worktree) {
		if head, digest, err := worktreeState(ctx, ws.worktree); err == nil {
			all[worktreeKey] = Artifact{File: ws.worktree, Verdict: "HEAD: " + head, Summary: digest}
		}
	}
	artifacts, _ := json.Marshal(all)
	n, err := w.Store.Q.RequeueForQuota(ctx, db.RequeueForQuotaParams{
		ID: e.ID, RunAfter: pgtype.Timestamptz{Time: reset.Add(quotaMargin), Valid: true}, WaitReason: text(QuotaWaitPrefix + formatReset(reset)),
		Artifacts: artifacts, CostUsd: store.NanoUSDToNumeric(int64(o.run.CostUSD * 1e9)), NumTurns: int32(o.run.NumTurns), //nolint:gosec // contador pequeno
	})
	if err != nil || n == 0 {
		w.log().Error("devolvendo o job à fila pela cota", "execução", e.ID, "erro", err, "linhas", n)
		return false
	}
	w.recordCalls(ctx, e, o.run, "sentinela-fix")
	w.log().Warn("cota da assinatura: job devolvido à fila", "execução", e.ID, "issue", e.IssueIdentifier, "até", formatReset(reset))
	w.pauseForQuota(ctx, reset, msg, fmt.Sprintf("job %d · %s", e.ID, e.IssueIdentifier))
	return true
}

// pauseForQuota pausa o worker até o reset (com a margem) e avisa uma vez no canal padrão.
func (w *Worker) pauseForQuota(ctx context.Context, reset time.Time, msg, what string) {
	until := reset.Add(quotaMargin)
	w.quota.mu.Lock()
	already := w.quota.until.After(w.now())
	if until.After(w.quota.until) {
		w.quota.until = until
	}
	w.quota.mu.Unlock()
	if already || w.Notify == nil {
		return
	}
	text := fmt.Sprintf("Sentinela · cota da assinatura do Claude Code esgotada (%s): o worker não pega jobs até %s. "+
		"Os jobs voltam para a fila com a label, sem falha, e continuam depois do reset. Mensagem: %s",
		what, formatReset(reset), notify.Escape(truncate(msg, 200)))
	for _, wmsg := range w.Notify.Summary(ctx, text) {
		w.log().Warn("slack", "aviso", wmsg)
	}
}

// quotaMarker devolve quando o job parou pela cota e a mensagem, se ele voltou à fila assim.
func quotaMarker(e db.Execution) (Artifact, time.Time, bool) {
	var all map[string]Artifact
	if json.Unmarshal(e.Artifacts, &all) != nil {
		return Artifact{}, time.Time{}, false
	}
	a, ok := all[quotaKey]
	if !ok {
		return Artifact{}, time.Time{}, false
	}
	at, _ := time.Parse(time.RFC3339, a.File)
	return a, at, true
}

// planQuotaContinue decide se o job devolvido pela cota continua a própria sessão: só se a sessão,
// o worktree e o diretório do job existem e o worktree está como ele deixou. Não conta como
// retomada (execution.max_resumes): a sessão não falhou.
func (w *Worker) planQuotaContinue(ctx context.Context, e db.Execution) ResumeDecision {
	d := ResumeDecision{Kind: FailUsageLimit}
	fresh := func(why string) ResumeDecision {
		d.Note = "recomeçou do zero depois da pausa pela cota: " + why
		return d
	}
	switch {
	case !e.SessionID.Valid || e.SessionID.String == "":
		return fresh("a sessão não foi gravada")
	case !isDir(e.Worktree.String):
		return fresh(fmt.Sprintf("o worktree %s não existe mais", e.Worktree.String))
	case !isDir(e.JobDir.String):
		return fresh(fmt.Sprintf("o diretório do job %s não existe mais", e.JobDir.String))
	}
	if _, found := FindSession(w.claudeHome(), e.Worktree.String, e.SessionID.String); !found {
		return fresh(fmt.Sprintf("a sessão %s não existe mais", e.SessionID.String))
	}
	if why := worktreeChanged(ctx, e); why != "" {
		return fresh(why)
	}
	d.Resume = true
	d.Note = fmt.Sprintf("continuou a sessão %s depois da pausa pela cota da assinatura", e.SessionID.String)
	return d
}
