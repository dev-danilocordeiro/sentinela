package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dev-danilocordeiro/sentinela/internal/signals"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// Fila consciente de conflitos (ADR 0031). Antes de pegar um job, o worker compara os caminhos do
// achado (scope_paths e arquivos de evidência) com os jobs do mesmo repo que vieram antes: em
// execução, com PR aberto ou mais à frente na fila. Com caminho em comum, o job espera e o motivo
// fica em executions.wait_reason; sem sobreposição, roda em paralelo, dentro da concorrência.

// QueueJob é uma linha da fila vista pelo planejamento.
type QueueJob struct {
	ID        int64
	FindingID int64
	Repo      string
	Issue     string
	Status    string // queued | running | succeeded | failed | canceled
	PRNumber  int32  // 0 sem PR
	PROpen    bool
	Ready     bool // run_after já passou
	// FindingHasOpenPR: o achado já tem PR aberto (rework ou nova tentativa na mesma branch). Na
	// fila, ele passa à frente dos jobs que esperam por esse PR, senão os dois se esperariam.
	FindingHasOpenPR bool
	Paths            []string
}

// QueuePlan diz quais jobs podem começar, em ordem, e por que os outros esperam.
type QueuePlan struct {
	Claimable []int64
	Waits     map[int64]string
}

// PlanQueue decide a fila. Seguram arquivos: jobs rodando e jobs com PR aberto, até o PR ser
// mergeado ou fechado. Na fila, um job espera também pelos que estão à frente dele com caminho em
// comum, para que a ordem de chegada se mantenha entre jobs que mexem nos mesmos arquivos.
func PlanQueue(jobs []QueueJob) QueuePlan {
	plan := QueuePlan{Waits: map[int64]string{}}
	var holders, queued []QueueJob
	for _, j := range jobs {
		switch {
		case j.Status == "queued":
			queued = append(queued, j)
		case j.Status == "running" || j.PROpen:
			holders = append(holders, j)
		}
	}
	// Ordem estável: quem já tem PR aberto primeiro; o resto na ordem da consulta (run_after, id).
	ordered := make([]QueueJob, 0, len(queued))
	for _, j := range queued {
		if j.FindingHasOpenPR {
			ordered = append(ordered, j)
		}
	}
	for _, j := range queued {
		if !j.FindingHasOpenPR {
			ordered = append(ordered, j)
		}
	}
	for i, j := range ordered {
		reason := ""
		for _, h := range holders {
			if r := waitReason(j, h, holderState(h)); r != "" {
				reason = r
				break
			}
		}
		for _, ahead := range ordered[:i] {
			if reason != "" {
				break
			}
			reason = waitReason(j, ahead, "na fila antes")
		}
		switch {
		case reason != "":
			plan.Waits[j.ID] = reason
		case j.Ready:
			plan.Claimable = append(plan.Claimable, j.ID)
		}
	}
	return plan
}

func holderState(h QueueJob) string {
	if h.Status == "running" {
		return "em execução"
	}
	if h.PRNumber > 0 {
		return fmt.Sprintf("PR #%d aberto", h.PRNumber)
	}
	return "PR aberto"
}

// waitReason devolve o motivo de j esperar por other, ou "" se não houver conflito.
func waitReason(j, other QueueJob, state string) string {
	if j.ID == other.ID || j.FindingID == other.FindingID || j.Repo != other.Repo {
		return ""
	}
	common, ok := overlap(j.Paths, other.Paths)
	if !ok {
		return ""
	}
	who := fmt.Sprintf("job %d", other.ID)
	if other.Issue != "" {
		who += " (" + other.Issue + ")"
	}
	return fmt.Sprintf("aguarda %s, %s: caminho em comum %s", who, state, common)
}

// overlap diz se dois conjuntos de caminhos se cruzam e devolve o caminho mais específico do
// primeiro par que cruza. Um caminho é arquivo ou diretório (prefixo) e pode ser glob. Sem nenhum
// caminho, o achado pode tocar o repo inteiro: conflita com tudo.
func overlap(a, b []string) (string, bool) {
	if len(a) == 0 {
		a = []string{"."}
	}
	if len(b) == 0 {
		b = []string{"."}
	}
	for _, pa := range a {
		for _, pb := range b {
			if c, ok := pathsOverlap(normPath(pa), normPath(pb)); ok {
				return c, true
			}
		}
	}
	return "", false
}

func normPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "./")
	p = strings.Trim(p, "/")
	if p == "" {
		return "."
	}
	return path.Clean(p)
}

func pathsOverlap(a, b string) (string, bool) {
	switch {
	case a == "." && b == ".":
		return "(repo inteiro)", true
	case a == ".":
		return b, true
	case b == ".":
		return a, true
	case a == b:
		return a, true
	case strings.HasPrefix(a, b+"/"):
		return a, true
	case strings.HasPrefix(b, a+"/"):
		return b, true
	}
	if isGlob(a) || isGlob(b) {
		if ok, _ := doublestar.Match(a, b); ok {
			return b, true
		}
		if ok, _ := doublestar.Match(b, a); ok {
			return a, true
		}
		// Glob contra diretório: compara o prefixo fixo do glob.
		if isGlob(a) && !isGlob(b) {
			return globVsDir(a, b)
		}
		if isGlob(b) && !isGlob(a) {
			return globVsDir(b, a)
		}
	}
	return "", false
}

// globVsDir cobre "services/billing/**" contra "services": o prefixo fixo do glob fica dentro do
// diretório (ou o contrário).
func globVsDir(glob, dir string) (string, bool) {
	base, _ := doublestar.SplitPattern(glob)
	base = normPath(base)
	if base == "." {
		return glob, true
	}
	if c, ok := pathsOverlap(base, dir); ok {
		if c == base {
			return glob, true
		}
		return c, true
	}
	return "", false
}

func isGlob(p string) bool { return strings.ContainsAny(p, "*?[{") }

// jobPaths junta scope_paths e os arquivos de evidência do achado.
func jobPaths(scope []string, evidence []byte) []string {
	out := append([]string(nil), scope...)
	var ev []signals.Evidence
	if len(evidence) > 0 && json.Unmarshal(evidence, &ev) == nil {
		for _, e := range ev {
			if e.File != "" {
				out = append(out, e.File)
			}
		}
	}
	return out
}

func queueJobs(rows []db.QueueSnapshotRow) []QueueJob {
	jobs := make([]QueueJob, 0, len(rows))
	for _, r := range rows {
		jobs = append(jobs, QueueJob{
			ID: r.ID, FindingID: r.FindingID, Repo: r.Repo, Issue: r.IssueIdentifier, Status: r.Status,
			PRNumber: r.PrNumber.Int32, PROpen: r.PrState.Valid && r.PrState.String == "open", Ready: r.Ready,
			FindingHasOpenPR: r.FindingHasOpenPr, Paths: jobPaths(r.ScopePaths, r.Evidence),
		})
	}
	return jobs
}

// claim planeja a fila e pega o primeiro job livre, numa transação com trava consultiva: dois
// workers (goroutines ou processos) não escolhem ao mesmo tempo dois jobs que se cruzam. Grava o
// motivo de espera dos outros. Devolve false quando nenhum job pode começar agora.
func (w *Worker) claim(ctx context.Context) (db.Execution, bool, error) {
	var claimed db.Execution
	found := false
	err := pgx.BeginFunc(ctx, w.Store.Pool, func(tx pgx.Tx) error {
		q := w.Store.Q.WithTx(tx)
		if err := q.LockQueue(ctx); err != nil {
			return err
		}
		rows, err := q.QueueSnapshot(ctx)
		if err != nil {
			return err
		}
		plan := PlanQueue(queueJobs(rows))
		for _, r := range rows {
			if r.Status != "queued" {
				continue
			}
			reason, waits := plan.Waits[r.ID]
			if !r.Ready && strings.HasPrefix(r.WaitReason.String, QuotaWaitPrefix) {
				continue // devolvido pela cota: o motivo fica até o reset
			}
			if err := q.SetWaitReason(ctx, db.SetWaitReasonParams{ID: r.ID, WaitReason: pgtype.Text{String: reason, Valid: waits}}); err != nil {
				return err
			}
		}
		for _, id := range plan.Claimable {
			e, err := q.ClaimExecutionByID(ctx, id)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			claimed, found = e, true
			return nil
		}
		return nil
	})
	return claimed, found, err
}

// Waiting lista os jobs da fila que esperam por conflito, com o motivo.
func (w *Worker) Waiting(ctx context.Context) ([]db.QueueSnapshotRow, error) {
	rows, err := w.Store.Q.QueueSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	var out []db.QueueSnapshotRow
	for _, r := range rows {
		if r.Status == "queued" && r.WaitReason.Valid {
			out = append(out, r)
		}
	}
	return out, nil
}
