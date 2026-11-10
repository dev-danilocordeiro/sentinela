package linear

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// Syncer traz de volta o estado das issues publicadas: issue cancelada vira achado
// rejected com supressão; issue concluída vira fixed. Os dois gatilhos (polling e webhook)
// passam pelo mesmo Apply e são deduplicados por event_id em webhook_events.
type Syncer struct {
	Store  *store.Store
	Linear Tracker
	// Cfg dá as labels de resposta às perguntas (achados com premissa de produto); nil desliga.
	Cfg *config.Config
}

// Change é uma mudança aplicada a um achado.
type Change struct {
	FindingID     int64  `json:"finding_id"`
	Identifier    string `json:"identifier"`
	State         string `json:"state"`   // estado no Linear
	Outcome       string `json:"outcome"` // rejected | accepted | duplicate_of | "" (só atualizou o estado)
	Reason        string `json:"reason,omitempty"`
	Actor         string `json:"actor,omitempty"`
	SuppressionID int64  `json:"suppression_id,omitempty"`
	DuplicateOf   string `json:"duplicate_of,omitempty"` // issue original (Duplicate humano)
}

// SyncResult resume uma passada de polling.
type SyncResult struct {
	Checked    int      `json:"checked"`
	Duplicates int      `json:"duplicates"` // eventos já processados (mesmo updatedAt)
	Changes    []Change `json:"changes"`
}

// Poll lê o estado de todas as issues principais cujo achado ainda espera decisão.
// O event_id do polling é "poll:<issue>:<updatedAt>": a mesma versão da issue só é
// aplicada uma vez, mesmo com duas instâncias rodando. O evento só fica marcado como processado
// depois de a decisão ser aplicada (na transação dela): se o Linear falhar no meio, o próximo poll
// aplica de novo, e as guardas de estado evitam outcome ou supressão em dobro.
func (s *Syncer) Poll(ctx context.Context) (SyncResult, error) {
	var res SyncResult
	tracked, err := s.Store.Q.TrackedIssues(ctx)
	if err != nil || len(tracked) == 0 {
		return res, err
	}
	ids := make([]string, 0, len(tracked))
	for _, t := range tracked {
		ids = append(ids, t.LinearID)
	}
	issues, err := s.Linear.Issues(ctx, ids)
	if err != nil {
		return res, err
	}
	for _, is := range issues {
		res.Checked++
		payload, err := json.Marshal(is)
		if err != nil {
			return res, err
		}
		ev := syncEvent{id: "poll:" + is.ID + ":" + is.UpdatedAt.UTC().Format(time.RFC3339Nano), typ: "IssuePoll", payload: payload}
		ch, fresh, err := s.applyEvent(ctx, ev, []Issue{is})
		if err != nil {
			return res, err
		}
		if !fresh {
			res.Duplicates++
			continue
		}
		res.Changes = append(res.Changes, ch...)
	}
	return res, nil
}

// syncEvent é o evento (poll ou entrega do webhook) que leva a uma aplicação.
type syncEvent struct {
	id, typ string
	payload []byte
}

type eventKey struct{}

// markIn grava o evento do contexto como processado. Chamado dentro da transação de cada decisão
// e, de novo, depois da aplicação (idempotente).
func markIn(ctx context.Context, q *db.Queries) error {
	ev, ok := ctx.Value(eventKey{}).(*syncEvent)
	if !ok || ev == nil {
		return nil
	}
	return q.MarkEventProcessed(ctx, db.MarkEventProcessedParams{Provider: "linear", EventID: ev.id, EventType: ev.typ, Payload: ev.payload})
}

// applyEvent aplica as issues de um evento que ainda não foi processado; fresh=false quando já
// foi. Um erro deixa o evento sem processed_at, para o poll ou a reentrega seguinte.
func (s *Syncer) applyEvent(ctx context.Context, ev syncEvent, issues []Issue) ([]Change, bool, error) {
	done, err := s.Store.Q.WebhookEventProcessed(ctx, db.WebhookEventProcessedParams{Provider: "linear", EventID: ev.id})
	if err != nil || done {
		return nil, false, err
	}
	ctx = context.WithValue(ctx, eventKey{}, &ev)
	var out []Change
	for _, is := range issues {
		ch, err := s.Apply(ctx, is)
		if err != nil {
			return out, true, err
		}
		if ch != nil {
			out = append(out, *ch)
		}
	}
	return out, true, markIn(ctx, s.Store.Q)
}

// Apply aplica o estado atual de uma issue ao achado dela. Devolve nil se a issue não é do
// Sentinela ou se nada mudou.
func (s *Syncer) Apply(ctx context.Context, is Issue) (*Change, error) {
	li, err := s.Store.Q.LinearIssueByLinearID(ctx, is.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	stateChanged := li.StateType != is.State.Type || li.StateName != is.State.Name
	if stateChanged {
		if err := s.Store.Q.UpdateLinearIssueState(ctx, db.UpdateLinearIssueStateParams{LinearID: is.ID, StateName: is.State.Name, StateType: is.State.Type}); err != nil {
			return nil, err
		}
	}
	ch := &Change{FindingID: li.FindingID, Identifier: li.Identifier, State: is.State.Name}
	pending := li.FindingState == "open" || li.FindingState == "executing"
	if li.Role != "primary" || !pending {
		return changeOrNil(ch, stateChanged), nil
	}
	if s.Cfg != nil && li.PremiseAnswer == "not_intentional" && s.Cfg.Labels.Question != "" && is.HasLabel(s.Cfg.Labels.Question) &&
		is.State.Type != "canceled" && is.State.Type != "duplicate" {
		// A resposta já foi gravada, mas o Linear falhou antes de a issue virar o achado: termina
		// agora, sem outro outcome.
		return ch, s.finishNotIntentional(ctx, li, is, ch)
	}
	if s.Cfg != nil && li.Premise != "" && li.PremiseAnswer == "" && is.State.Type != "canceled" && is.State.Type != "duplicate" {
		switch {
		case s.Cfg.Labels.NotIntentional != "" && is.HasLabel(s.Cfg.Labels.NotIntentional):
			return ch, s.answerNotIntentional(ctx, li, is, ch)
		case s.Cfg.Labels.Intentional != "" && is.HasLabel(s.Cfg.Labels.Intentional):
			return ch, s.answerIntentional(ctx, li, is, ch)
		}
	}
	switch is.State.Type {
	case "duplicate":
		// Duplicate não é recusa do problema: é um merge humano para a issue original. Sem
		// supressão nem exemplo de recusa; o achado acompanha o original (ADR 0020).
		if err := s.duplicate(ctx, li, is, ch); err != nil {
			return nil, err
		}
		return ch, nil
	case "canceled":
		cl, err := s.Linear.Closure(ctx, is.ID)
		if err != nil {
			return nil, err
		}
		ch.Outcome, ch.Actor = "rejected", cl.Actor
		ch.Reason = cl.Reason
		if ch.Reason == "" {
			ch.Reason = fmt.Sprintf("issue %s movida para %s no Linear, sem comentário", li.Identifier, is.State.Name)
		}
		if err := s.resolve(ctx, li, "rejected", ch); err != nil {
			return nil, err
		}
		return ch, nil
	case "completed":
		ch.Outcome, ch.Reason = "accepted", fmt.Sprintf("issue %s concluída no Linear", li.Identifier)
		if err := s.resolve(ctx, li, "fixed", ch); err != nil {
			return nil, err
		}
		return ch, nil
	}
	return changeOrNil(ch, stateChanged), nil
}

func changeOrNil(ch *Change, changed bool) *Change {
	if changed {
		return ch
	}
	return nil
}

// duplicate grava o merge humano: outcome duplicate_of (decided_by human), achado ligado ao
// original e com o estado dele. Original fora do Sentinela (ou sem relação) deixa o achado stale.
func (s *Syncer) duplicate(ctx context.Context, li db.LinearIssueByLinearIDRow, is Issue, ch *Change) error {
	orig, err := s.Linear.DuplicateOf(ctx, is.ID)
	if err != nil {
		return err
	}
	cl, err := s.Linear.Closure(ctx, is.ID)
	if err != nil {
		return err
	}
	ch.Outcome, ch.Actor = "duplicate_of", cl.Actor
	var origID pgtype.Int8
	if orig == nil {
		ch.Reason = fmt.Sprintf("%s marcada como Duplicate no Linear, sem issue original na relação", li.Identifier)
	} else {
		ch.DuplicateOf = orig.Identifier
		ch.Reason = fmt.Sprintf("%s marcada como Duplicate de %s no Linear", li.Identifier, orig.Identifier)
		if o, err := s.Store.Q.LinearIssueByLinearID(ctx, orig.ID); err == nil {
			origID = pgtype.Int8{Int64: o.FindingID, Valid: true}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		} else {
			ch.Reason += " (issue fora do Sentinela)"
		}
	}
	return pgx.BeginFunc(ctx, s.Store.Pool, func(tx pgx.Tx) error {
		q := s.Store.Q.WithTx(tx)
		if err := markIn(ctx, q); err != nil {
			return err
		}
		if _, err := q.MarkFindingDuplicate(ctx, db.MarkFindingDuplicateParams{ID: li.FindingID, OriginalID: origID, Reason: pgtype.Text{String: ch.Reason, Valid: true}}); errors.Is(err, pgx.ErrNoRows) {
			ch.Outcome = ""
			return nil
		} else if err != nil {
			return err
		}
		return q.InsertHumanOutcome(ctx, db.InsertHumanOutcomeParams{
			FindingID: li.FindingID, Kind: "duplicate_of",
			Reason: pgtype.Text{String: ch.Reason, Valid: true}, Actor: pgtype.Text{String: ch.Actor, Valid: ch.Actor != ""},
		})
	})
}

// answerNotIntentional: o time respondeu que o comportamento não é intencional. A pergunta vira
// o achado normal na mesma issue (título, corpo com critérios) e a label de pergunta sai.
func (s *Syncer) answerNotIntentional(ctx context.Context, li db.LinearIssueByLinearIDRow, is Issue, ch *Change) error {
	// Quem aplicou a label, quando o tracker sabe dizer (a API sabe; o MCP não).
	actor := ""
	if ev, found, err := s.Linear.TriggerActor(ctx, is.ID, s.Cfg.Labels.NotIntentional); err == nil && found {
		actor = ev.Actor.Name
		if actor == "" {
			actor = ev.Actor.Email
		}
	} else if err != nil && !errors.Is(err, ErrUnsupported) {
		return err
	}
	reason := "resposta à pergunta: não é intencional; a pergunta virou o achado (label " + s.Cfg.Labels.NotIntentional + ")"
	// A resposta e o outcome accepted (decided_by human), numa transação: os três modos gravam o
	// mesmo que o `finding answer not-intentional` do modo local (ADR 0045).
	answered := true
	err := pgx.BeginFunc(ctx, s.Store.Pool, func(tx pgx.Tx) error {
		q := s.Store.Q.WithTx(tx)
		if _, err := q.SetPremiseAnswer(ctx, db.SetPremiseAnswerParams{ID: li.FindingID, Answer: pgtype.Text{String: "not_intentional", Valid: true}}); errors.Is(err, pgx.ErrNoRows) {
			answered = false
			return nil
		} else if err != nil {
			return err
		}
		return q.InsertHumanOutcome(ctx, db.InsertHumanOutcomeParams{
			FindingID: li.FindingID, Kind: "accepted",
			Reason: pgtype.Text{String: reason, Valid: true}, Actor: pgtype.Text{String: actor, Valid: actor != ""},
		})
	})
	if err != nil || !answered {
		return err
	}
	ch.Outcome, ch.Reason, ch.Actor = "accepted", reason, actor
	return s.finishNotIntentional(ctx, li, is, ch)
}

// finishNotIntentional é a parte no Linear da resposta "não é intencional": a issue ganha o título
// e o corpo do achado e perde a label de pergunta. Sem a label, uma nova aplicação não faz nada.
func (s *Syncer) finishNotIntentional(ctx context.Context, li db.LinearIssueByLinearIDRow, is Issue, ch *Change) error {
	row, err := s.Store.Q.GetFinding(ctx, li.FindingID)
	if err != nil {
		return err
	}
	f, err := findingFromDB(row)
	if err != nil {
		return err
	}
	refs, err := s.Linear.TeamRefs(ctx, li.LinearTeam)
	if err != nil {
		return err
	}
	var remove []string
	if ids, err := s.Linear.LabelIDs(ctx, refs.ID, []string{s.Cfg.Labels.Question}); err == nil {
		remove = ids
	}
	j := judgeInfo{Confidence: float64(row.JudgeConfidence.Float32), Valid: row.JudgeConfidence.Valid, Reason: row.JudgeReason.String}
	in, err := LoadInstall(ctx, s.Store.Q)
	if err != nil {
		return err
	}
	body := issueBody(f, installMarker(primaryMarker(f.Fingerprint), in.ID), j, nil, nil)
	if err := s.Linear.UpdateIssue(ctx, is.ID, f.Title, body, nil, remove); err != nil {
		return err
	}
	return s.Linear.Comment(ctx, is.ID, "**Sentinela:** resposta registrada (não é intencional). A pergunta virou o achado \""+f.Title+"\", com a correção e os critérios de aceite.")
}

// answerIntentional: o time respondeu que é intencional. Vira recusa com supressão e o motivo
// (último comentário humano). A issue não é fechada pelo Sentinela: fechar é decisão humana.
func (s *Syncer) answerIntentional(ctx context.Context, li db.LinearIssueByLinearIDRow, is Issue, ch *Change) error {
	cl, err := s.Linear.Closure(ctx, is.ID)
	if err != nil {
		return err
	}
	ch.Outcome, ch.Actor = "rejected", cl.Actor
	ch.Reason = "É intencional"
	if strings.TrimSpace(cl.Reason) != "" {
		ch.Reason += ": " + strings.TrimSpace(cl.Reason)
	} else {
		ch.Reason += " (resposta pela label " + s.Cfg.Labels.Intentional + ", sem comentário)"
	}
	// A resposta, a recusa e a supressão numa transação só: um erro no meio não deixa a resposta
	// gravada sem a recusa (que a guarda de premise_answer nunca mais aplicaria).
	if err := pgx.BeginFunc(ctx, s.Store.Pool, func(tx pgx.Tx) error {
		q := s.Store.Q.WithTx(tx)
		if _, err := q.SetPremiseAnswer(ctx, db.SetPremiseAnswerParams{ID: li.FindingID, Answer: pgtype.Text{String: "intentional", Valid: true}}); errors.Is(err, pgx.ErrNoRows) {
			ch.Outcome = ""
			return markIn(ctx, q)
		} else if err != nil {
			return err
		}
		return s.resolveTx(ctx, q, li, "rejected", ch)
	}); err != nil || ch.Outcome == "" {
		return err
	}
	return s.Linear.Comment(ctx, is.ID, "**Sentinela:** resposta registrada (é intencional). O motivo virou supressão e a pergunta não volta. Fechar a issue fica com o time.")
}

// resolve muda o achado, grava o desfecho e, na recusa, a supressão pelo fingerprint, numa
// transação. Se outro processo já resolveu o achado, não faz nada.
func (s *Syncer) resolve(ctx context.Context, li db.LinearIssueByLinearIDRow, state string, ch *Change) error {
	return pgx.BeginFunc(ctx, s.Store.Pool, func(tx pgx.Tx) error {
		return s.resolveTx(ctx, s.Store.Q.WithTx(tx), li, state, ch)
	})
}

// resolveTx é o resolve dentro de uma transação já aberta; grava também o evento como processado.
func (s *Syncer) resolveTx(ctx context.Context, q *db.Queries, li db.LinearIssueByLinearIDRow, state string, ch *Change) error {
	if err := markIn(ctx, q); err != nil {
		return err
	}
	{
		_, err := q.ResolveFinding(ctx, db.ResolveFindingParams{ID: li.FindingID, State: state, Reason: pgtype.Text{String: ch.Reason, Valid: true}})
		if errors.Is(err, pgx.ErrNoRows) {
			ch.Outcome = ""
			return nil
		}
		if err != nil {
			return err
		}
		already := false
		if ch.Outcome == "accepted" {
			// Já aceito por uma pessoa (ex.: resposta "não é intencional"): fecha como fixed sem um
			// segundo accepted, que contaria duas vezes nas métricas e no Curator.
			if already, err = q.HasHumanAccepted(ctx, li.FindingID); err != nil {
				return err
			}
		}
		if already {
			ch.Outcome = ""
		} else if err := q.InsertOutcome(ctx, db.InsertOutcomeParams{
			FindingID: li.FindingID, Kind: ch.Outcome,
			Reason: pgtype.Text{String: ch.Reason, Valid: true}, Actor: pgtype.Text{String: ch.Actor, Valid: ch.Actor != ""},
		}); err != nil {
			return err
		}
		// Duplicatas humanas deste achado acompanham o novo estado.
		if err := q.PropagateMergedState(ctx, li.FindingID); err != nil {
			return err
		}
		if state != "rejected" {
			return nil
		}
		sup, err := q.InsertSuppression(ctx, db.InsertSuppressionParams{
			Repo: li.Repo, Module: li.FindingModule, Category: pgtype.Text{String: li.Category, Valid: true},
			Fingerprint: pgtype.Text{String: li.Fingerprint, Valid: true}, Reason: ch.Reason,
			FindingID: pgtype.Int8{Int64: li.FindingID, Valid: true},
		})
		if err != nil {
			return err
		}
		ch.SuppressionID = sup.ID
		return nil
	}
}
