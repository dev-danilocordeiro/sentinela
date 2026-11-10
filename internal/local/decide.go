// Package local é o tracker local (adapters.tracker local, ADR 0038): os achados viram um
// relatório em markdown e HTML em adapters.local_dir, e as decisões humanas entram pela CLI
// (`sentinela finding accept|reject|fix`). O Postgres é o mesmo dos outros modos: as decisões
// gravam os mesmos outcomes (decided_by human), supressões e execuções que o sync do Linear.
package local

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// Executores de uma decisão humana (princípio 6): a pessoa ou um agente a pedido explícito dela.
const (
	ByHuman = "human"
	ByAgent = "agent"
)

// Decision é quem decidiu e quem executou.
type Decision struct {
	Actor      string // quem decidiu (padrão: $USER)
	ExecutedBy string // human (padrão) | agent
	Note       string // obrigatória com agent
}

func (d Decision) check() error {
	switch d.ExecutedBy {
	case "", ByHuman:
	case ByAgent:
		if strings.TrimSpace(d.Note) == "" {
			return errors.New("--executed-by agent exige --note dizendo quem pediu e por quê")
		}
	default:
		return fmt.Errorf("--executed-by inválido %q (use human ou agent)", d.ExecutedBy)
	}
	return nil
}

func (d Decision) executedBy() string {
	if d.ExecutedBy == "" {
		return ByHuman
	}
	return d.ExecutedBy
}

// Tracker grava decisões e escreve o relatório local.
type Tracker struct {
	Cfg   *config.Config
	Store *store.Store
	// ConfigFlag entra nos comandos do relatório (ex.: "--config sentinela.eval.yaml"); vazio
	// quando a config é a padrão.
	ConfigFlag string
}

// ErrNotLocal: a decisão é no tracker configurado (Linear), não na CLI.
var ErrNotLocal = errors.New("adapters.tracker não é local: decida na issue do Linear (a CLI só decide no modo local)")

func (t *Tracker) open(ctx context.Context, id int64) (db.Finding, error) {
	if !t.Cfg.LocalTracker() {
		return db.Finding{}, ErrNotLocal
	}
	f, err := t.Store.Q.GetFinding(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, fmt.Errorf("achado %d não existe", id)
	}
	if err != nil {
		return f, err
	}
	if f.MergedInto.Valid {
		return f, fmt.Errorf("achado %d foi mesclado no %d: decida no original", id, f.MergedInto.Int64)
	}
	if f.State != "open" && f.State != "executing" {
		return f, fmt.Errorf("achado %d já está %s", id, f.State)
	}
	return f, nil
}

func (t *Tracker) accepted(ctx context.Context, q *db.Queries, id int64) (bool, error) {
	outs, err := q.FindingOutcomes(ctx, id)
	if err != nil {
		return false, err
	}
	for _, o := range outs {
		if o.Kind == "accepted" && o.DecidedBy == ByHuman {
			return true, nil
		}
	}
	return false, nil
}

func outcome(id int64, kind, reason string, d Decision) db.InsertDecisionOutcomeParams {
	return db.InsertDecisionOutcomeParams{
		FindingID: id, Kind: kind, Reason: pgtype.Text{String: reason, Valid: reason != ""},
		Actor: pgtype.Text{String: d.Actor, Valid: d.Actor != ""}, ExecutedBy: d.executedBy(),
		Note: pgtype.Text{String: d.Note, Valid: d.Note != ""},
	}
}

// Accept registra que uma pessoa aceitou o achado como problema real. O achado continua aberto,
// esperando a correção (fix) ou a resolução fora do Sentinela; vira exemplo aceito no Curator.
func (t *Tracker) Accept(ctx context.Context, id int64, d Decision) error {
	if err := d.check(); err != nil {
		return err
	}
	if _, err := t.open(ctx, id); err != nil {
		return err
	}
	// Conferir e gravar na mesma transação, com o achado travado: dois accept ao mesmo tempo
	// gravam um accepted só.
	return pgx.BeginFunc(ctx, t.Store.Pool, func(tx pgx.Tx) error {
		q := t.Store.Q.WithTx(tx)
		state, err := q.LockFinding(ctx, id)
		if err != nil {
			return err
		}
		if state != "open" && state != "executing" {
			return fmt.Errorf("achado %d foi resolvido por outro processo (%s)", id, state)
		}
		if ok, err := t.accepted(ctx, q, id); err != nil {
			return err
		} else if ok {
			return fmt.Errorf("achado %d já foi aceito", id)
		}
		return q.InsertDecisionOutcome(ctx, outcome(id, "accepted", "aceito pela CLI (sentinela finding accept)", d))
	})
}

// Reject é o equivalente de cancelar a issue no Linear: achado rejected, outcome com o motivo e
// supressão pelo fingerprint, numa transação.
func (t *Tracker) Reject(ctx context.Context, id int64, reason string, d Decision) (int64, error) {
	if err := d.check(); err != nil {
		return 0, err
	}
	if strings.TrimSpace(reason) == "" {
		return 0, errors.New("o motivo da recusa é obrigatório: ele vira a supressão e ensina o Judge")
	}
	f, err := t.open(ctx, id)
	if err != nil {
		return 0, err
	}
	var supID int64
	err = pgx.BeginFunc(ctx, t.Store.Pool, func(tx pgx.Tx) error {
		q := t.Store.Q.WithTx(tx)
		if _, err := q.ResolveFinding(ctx, db.ResolveFindingParams{ID: id, State: "rejected", Reason: pgtype.Text{String: reason, Valid: true}}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("achado %d foi resolvido por outro processo", id)
			}
			return err
		}
		if err := q.InsertDecisionOutcome(ctx, outcome(id, "rejected", reason, d)); err != nil {
			return err
		}
		if err := q.PropagateMergedState(ctx, id); err != nil {
			return err
		}
		sup, err := q.InsertSuppression(ctx, db.InsertSuppressionParams{
			Repo: f.Repo, Module: f.Module, Category: pgtype.Text{String: f.Category, Valid: true},
			Fingerprint: pgtype.Text{String: f.Fingerprint, Valid: true}, Reason: reason,
			FindingID: pgtype.Int8{Int64: id, Valid: true},
		})
		supID = sup.ID
		return err
	})
	return supID, err
}

// Fix é o equivalente de aplicar a label de gatilho: enfileira a execução do achado (o worker
// pega no próximo ciclo). Registra também a aceitação, se ainda não houver: pedir a correção é
// aceitar o problema. Depois de um job que falhou ou foi bloqueado sem PR, é uma nova tentativa
// dele (como `sentinela job retry`).
func (t *Tracker) Fix(ctx context.Context, id int64, d Decision) (db.Execution, error) {
	if err := d.check(); err != nil {
		return db.Execution{}, err
	}
	f, err := t.open(ctx, id)
	if err != nil {
		return db.Execution{}, err
	}
	params := db.EnqueueExecutionParams{FindingID: id, Kind: "fix", Repo: f.Repo, IssueIdentifier: config.LocalIssueID(id)}
	last, err := t.Store.Q.LastExecutionForFinding(ctx, id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return db.Execution{}, err
	case last.Status == "queued" || last.Status == "running":
		return db.Execution{}, fmt.Errorf("achado %d já tem o job %d %s", id, last.ID, last.Status)
	case last.PrNumber.Valid && last.PrState.String != "closed":
		return db.Execution{}, fmt.Errorf("achado %d já tem PR (%s): para mudar a correção use `sentinela job rework %d \"<nota>\"`", id, last.PrUrl.String, last.ID)
	case last.Status == "failed" || last.Decision.String == "blocked":
		params.PreviousID = pgtype.Int8{Int64: last.ID, Valid: true}
	}
	var e db.Execution
	err = pgx.BeginFunc(ctx, t.Store.Pool, func(tx pgx.Tx) error {
		q := t.Store.Q.WithTx(tx)
		var err error
		if e, err = q.EnqueueExecution(ctx, params); errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("achado %d já tem um job ativo", id)
		} else if err != nil {
			return err
		}
		if _, err := q.LockFinding(ctx, id); err != nil {
			return err
		}
		if ok, err := t.accepted(ctx, q, id); err != nil || ok {
			return err
		}
		return q.InsertDecisionOutcome(ctx, outcome(id, "accepted", "correção pedida pela CLI (sentinela finding fix)", d))
	})
	return e, err
}

// Resolve registra que a correção foi feita à mão, fora do Sentinela: o equivalente de concluir a
// issue no Linear. O achado vira fixed e o outcome accepted (decided_by human) leva a nota como
// motivo, numa transação. Com um job na fila ou rodando, recusa: cancele o job antes.
func (t *Tracker) Resolve(ctx context.Context, id int64, d Decision) error {
	if err := d.check(); err != nil {
		return err
	}
	if strings.TrimSpace(d.Note) == "" {
		return errors.New("--note é obrigatória: diga como e onde a correção foi feita")
	}
	if _, err := t.open(ctx, id); err != nil {
		return err
	}
	if last, err := t.Store.Q.LastExecutionForFinding(ctx, id); err == nil && (last.Status == "queued" || last.Status == "running") {
		return fmt.Errorf("achado %d tem o job %d %s: cancele com `sentinela job cancel %d` antes de resolver à mão", id, last.ID, last.Status, last.ID)
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	reason := "corrigido à mão (sentinela finding resolve): " + strings.TrimSpace(d.Note)
	return pgx.BeginFunc(ctx, t.Store.Pool, func(tx pgx.Tx) error {
		q := t.Store.Q.WithTx(tx)
		if _, err := q.ResolveFinding(ctx, db.ResolveFindingParams{ID: id, State: "fixed", Reason: pgtype.Text{String: reason, Valid: true}}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("achado %d foi resolvido por outro processo", id)
			}
			return err
		}
		if err := q.InsertDecisionOutcome(ctx, outcome(id, "accepted", reason, d)); err != nil {
			return err
		}
		return q.PropagateMergedState(ctx, id)
	})
}

// Duplicate é o merge humano de duplicata, como marcar Duplicate no Linear (ADR 0045): sem
// supressão nem exemplo de recusa; o achado mais novo é absorvido pelo mais antigo (merged_into),
// acompanha o estado dele e grava o outcome duplicate_of (decided_by human). Se a ordem vier
// trocada, o mais antigo continua sendo o original. Devolve (duplicata, original).
func (t *Tracker) Duplicate(ctx context.Context, id, of int64, d Decision) (int64, int64, error) {
	if err := d.check(); err != nil {
		return 0, 0, err
	}
	if id == of {
		return 0, 0, errors.New("um achado não é duplicata de si mesmo")
	}
	a, err := t.open(ctx, id)
	if err != nil {
		return 0, 0, err
	}
	b, err := t.open(ctx, of)
	if err != nil {
		return 0, 0, err
	}
	if a.Repo != b.Repo {
		return 0, 0, fmt.Errorf("achados de repos diferentes (%s e %s) não são duplicatas", a.Repo, b.Repo)
	}
	dup, orig := id, of
	if a.CreatedAt.Time.Before(b.CreatedAt.Time) || (a.CreatedAt.Time.Equal(b.CreatedAt.Time) && id < of) {
		dup, orig = of, id // o mais antigo absorve o mais novo
	}
	reason := fmt.Sprintf("%s marcado como duplicata de %s pela CLI (sentinela finding duplicate)", config.LocalIssueID(dup), config.LocalIssueID(orig))
	if d.Note != "" {
		reason += ": " + strings.TrimSpace(d.Note)
	}
	err = pgx.BeginFunc(ctx, t.Store.Pool, func(tx pgx.Tx) error {
		q := t.Store.Q.WithTx(tx)
		if _, err := q.MarkFindingDuplicate(ctx, db.MarkFindingDuplicateParams{ID: dup, OriginalID: pgtype.Int8{Int64: orig, Valid: true}, Reason: pgtype.Text{String: reason, Valid: true}}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("achado %d foi resolvido por outro processo", dup)
			}
			return err
		}
		return q.InsertDecisionOutcome(ctx, outcome(dup, "duplicate_of", reason, d))
	})
	return dup, orig, err
}

// Respostas à pergunta de produto (premissa) de um achado.
const (
	AnswerIntentional    = "intentional"
	AnswerNotIntentional = "not-intentional"
)

// Answer responde à pergunta de produto, como as labels sentinela-intencional e
// sentinela-nao-intencional no Linear (ADR 0045):
//   - intentional: o comportamento é de propósito. O achado vira rejected, com a supressão e o
//     motivo "É intencional: <nota>";
//   - not-intentional: é um problema de verdade. A pergunta vira achado normal, que continua
//     aberto esperando a correção, e grava o outcome accepted.
//
// As duas gravam decided_by human.
func (t *Tracker) Answer(ctx context.Context, id int64, answer string, d Decision) error {
	if err := d.check(); err != nil {
		return err
	}
	if answer != AnswerIntentional && answer != AnswerNotIntentional {
		return fmt.Errorf("resposta %q: use intentional ou not-intentional", answer)
	}
	f, err := t.open(ctx, id)
	if err != nil {
		return err
	}
	if !f.Premise.Valid || strings.TrimSpace(f.Premise.String) == "" {
		return fmt.Errorf("achado %d não depende de premissa de produto: não há pergunta a responder", id)
	}
	if f.PremiseAnswer.Valid {
		return fmt.Errorf("a pergunta do achado %d já foi respondida (%s)", id, f.PremiseAnswer.String)
	}
	note := strings.TrimSpace(d.Note)
	if answer == AnswerIntentional {
		reason := "É intencional"
		if note != "" {
			reason += ": " + note
		} else {
			reason += " (resposta pela CLI, sem nota)"
		}
		if _, err := t.Reject(ctx, id, reason, d); err != nil {
			return err
		}
		_, err := t.Store.Q.SetPremiseAnswer(ctx, db.SetPremiseAnswerParams{ID: id, Answer: pgtype.Text{String: "intentional", Valid: true}})
		return err
	}
	reason := "resposta à pergunta: não é intencional; a pergunta virou o achado"
	if note != "" {
		reason += ": " + note
	}
	return pgx.BeginFunc(ctx, t.Store.Pool, func(tx pgx.Tx) error {
		q := t.Store.Q.WithTx(tx)
		if _, err := q.SetPremiseAnswer(ctx, db.SetPremiseAnswerParams{ID: id, Answer: pgtype.Text{String: "not_intentional", Valid: true}}); err != nil {
			return err
		}
		return q.InsertDecisionOutcome(ctx, outcome(id, "accepted", reason, d))
	})
}
