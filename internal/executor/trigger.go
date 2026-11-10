package executor

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dev-danilocordeiro/sentinela/internal/gate"
	"github.com/dev-danilocordeiro/sentinela/internal/github"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// Enqueue confere no Linear as issues principais com achado aberto e sem execução, e enfileira
// as que têm a label de gatilho. Também enfileira uma nova tentativa quando a última execução
// falhou ou foi bloqueada sem PR e uma pessoa reaplicou a label depois disso (ADR 0029). Quem
// aplica a label é sempre um humano (princípio 6); o Sentinela só lê. Devolve as execuções
// criadas.
func (w *Worker) Enqueue(ctx context.Context) ([]db.Execution, error) {
	if w.Linear == nil {
		// Tracker local (ADR 0038): o pedido vem de `sentinela finding fix`, que já enfileira.
		return nil, nil
	}
	w.resetMembers() // fix_trigger: membros dos times lidos uma vez por ciclo (ADR 0042)
	out, err := w.enqueueFirst(ctx)
	if err != nil {
		return out, err
	}
	again, err := w.enqueueRetriggered(ctx)
	return append(out, again...), err
}

// triggerable devolve as issues (por id do Linear) com a label de gatilho do time dono (teams.<time>.labels.trigger
// ou labels.trigger) e ainda abertas. teams mapeia o id da issue para o time.
func (w *Worker) triggerable(ctx context.Context, teams map[string]string) (map[string]bool, error) {
	ids := make([]string, 0, len(teams))
	for id := range teams {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	issues, err := w.Linear.Issues(ctx, ids)
	if err != nil {
		return nil, err
	}
	labeled := map[string]bool{}
	for _, is := range issues {
		if is.HasLabel(w.Cfg.LabelsFor(teams[is.ID]).Trigger) && is.State.Type != "canceled" && is.State.Type != "duplicate" && is.State.Type != "completed" {
			labeled[is.ID] = true
		}
	}
	return labeled, nil
}

func (w *Worker) enqueueFirst(ctx context.Context) ([]db.Execution, error) {
	cands, err := w.Store.Q.TriggerCandidates(ctx)
	if err != nil || len(cands) == 0 {
		return nil, err
	}
	teams := make(map[string]string, len(cands))
	for _, c := range cands {
		teams[c.LinearID] = c.Team
	}
	labeled, err := w.triggerable(ctx, teams)
	if err != nil {
		return nil, err
	}
	var out []db.Execution
	for _, c := range cands {
		if !labeled[c.LinearID] {
			continue
		}
		if ok, err := w.authorizeTrigger(ctx, c.FindingID, c.LinearID, c.Identifier, c.Team, c.LinearTeam, w.Cfg.LabelsFor(c.Team).Trigger); err != nil {
			return out, err
		} else if !ok {
			continue
		}
		e, err := w.Store.Q.EnqueueExecution(ctx, db.EnqueueExecutionParams{
			FindingID: c.FindingID, Kind: "fix", Repo: c.Repo, IssueIdentifier: c.Identifier, LinearIssueID: c.LinearID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue // já há uma ativa
		}
		if err != nil {
			return out, err
		}
		out = append(out, e)
	}
	return out, nil
}

// enqueueRetriggered trata as issues cuja última execução falhou ou foi bloqueada sem PR (ADR
// 0029). Se o pedido ainda não foi consumido, tira a label de gatilho (consumeTrigger). Se já
// foi, a label de volta na issue é um novo pedido de uma pessoa: enfileira uma nova tentativa,
// como `job retry`. Os comentários feitos desde a execução anterior entram na spec como
// Instruções da equipe (ADR 0026).
//
// O gatilho não sai do histórico da issue: quando a mesma pessoa tira e recoloca a label em
// segundos, o Linear grava uma entrada só, sem label adicionada nem removida (visto na EX-15).
func (w *Worker) enqueueRetriggered(ctx context.Context) ([]db.Execution, error) {
	cands, err := w.Store.Q.RetriggerCandidates(ctx)
	if err != nil || len(cands) == 0 {
		return nil, err
	}
	teams := make(map[string]string, len(cands))
	for _, c := range cands {
		teams[c.LinearID] = c.Team
	}
	labeled, err := w.triggerable(ctx, teams)
	if err != nil {
		return nil, err
	}
	var out []db.Execution
	for _, c := range cands {
		if !c.TriggerConsumed {
			if err := w.consumeTrigger(ctx, c.PreviousID, c.LinearID, c.Identifier, c.LinearTeam, w.Cfg.LabelsFor(c.Team).Trigger, labeled[c.LinearID], true); err != nil {
				w.log().Warn("tirando a label de gatilho", "issue", c.Identifier, "erro", err)
			}
			continue
		}
		if !labeled[c.LinearID] {
			continue
		}
		if ok, err := w.authorizeTrigger(ctx, c.FindingID, c.LinearID, c.Identifier, c.Team, c.LinearTeam, w.Cfg.LabelsFor(c.Team).Trigger); err != nil {
			return out, err
		} else if !ok {
			continue
		}
		e, err := w.Store.Q.EnqueueExecution(ctx, db.EnqueueExecutionParams{
			FindingID: c.FindingID, Kind: "fix", Repo: c.Repo, IssueIdentifier: c.Identifier,
			LinearIssueID: c.LinearID, PreviousID: pgtype.Int8{Int64: c.PreviousID, Valid: true},
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue // já há uma ativa
		}
		if err != nil {
			return out, err
		}
		_ = w.comment(ctx, c.LinearID, fmt.Sprintf("**Sentinela · execução #%d**: nova tentativa da execução #%d, pedida ao aplicar `%s` de novo. Os comentários desde a execução anterior entram na spec como instruções da equipe.",
			e.ID, c.PreviousID, w.Cfg.LabelsFor(c.Team).Trigger))
		out = append(out, e)
	}
	return out, nil
}

// consumeTrigger tira a label de gatilho de uma issue cuja execução terminou sem PR e registra
// isso na execução (ADR 0029). Não é decisão: registra que o pedido foi atendido, e aplicar a
// label de novo vira um gesto único que o worker vê. Só registra depois de tirar a label, para
// não tomar a label antiga por um pedido novo. comment avisa na issue (o comentário do fim do job
// já avisa; o Enqueue, ao recuperar uma execução antiga, não).
func (w *Worker) consumeTrigger(ctx context.Context, execID int64, issueID, identifier, team, trigger string, labeled, comment bool) error {
	if labeled {
		refs, err := w.Linear.TeamRefs(ctx, team)
		if err != nil {
			return err
		}
		ids, err := w.Linear.LabelIDs(ctx, refs.ID, []string{trigger})
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return fmt.Errorf("label %s não existe no time %s", trigger, team)
		}
		if err := w.Linear.UpdateIssue(ctx, issueID, "", "", nil, ids); err != nil {
			return err
		}
	}
	a := Artifact{Verdict: "TRIGGER: consumed", Summary: fmt.Sprintf("label %s tirada pelo worker em %s", trigger, identifier)}
	if !labeled {
		a.Summary = fmt.Sprintf("label %s já estava fora de %s", trigger, identifier)
	}
	if err := w.Store.Q.MarkTriggerConsumed(ctx, db.MarkTriggerConsumedParams{ID: execID, Artifact: mustJSON(a)}); err != nil {
		return err
	}
	if labeled && comment {
		_ = w.comment(ctx, issueID, fmt.Sprintf("**Sentinela · execução #%d**: tirei a `%s` porque a execução terminou sem PR. Para tentar de novo, comente o que muda e aplique `%s` outra vez.",
			execID, trigger, trigger))
	}
	return nil
}

// Approve é `sentinela job approve <id>`: um humano assume a decisão de um job em ESCALATE. A
// revisora publica de novo a review do gate (COMMENT, enquanto a trava estiver ligada); o PR
// continua em draft e o merge continua com o humano.
func (w *Worker) Approve(ctx context.Context, id int64, by string) (db.Execution, *gate.Result, error) {
	if strings.TrimSpace(by) == "" {
		return db.Execution{}, nil, errors.New("informe quem aprova (--by)")
	}
	e, err := w.Store.Q.ApproveExecution(ctx, db.ApproveExecutionParams{ID: id, ApprovedBy: text(by)})
	if errors.Is(err, pgx.ErrNoRows) {
		cur, gerr := w.Store.Q.GetExecution(ctx, id)
		if gerr != nil {
			return db.Execution{}, nil, fmt.Errorf("job %d não existe", id)
		}
		return cur, nil, fmt.Errorf("job %d não está em ESCALATE (decisão %q) ou já foi aprovado por %s", id, cur.Decision.String, cur.ApprovedBy.String)
	}
	if err != nil {
		return e, nil, err
	}
	if !e.PrNumber.Valid {
		return e, nil, nil // ESCALATE sem PR (sem commits): só registra
	}
	f, err := w.finding(ctx, e.FindingID)
	if err != nil {
		return e, nil, err
	}
	rc, ok := w.Cfg.Repo(e.Repo)
	if !ok {
		return e, nil, fmt.Errorf("repo %s fora do sentinela.yaml", e.Repo)
	}
	ws := &workspace{rc: rc, worktree: e.Worktree.String, branch: e.Branch.String}
	if ws.base, err = w.Git.Run(ctx, ws.worktree, "merge-base", "origin/"+rc.DefaultBranch, "HEAD"); err != nil {
		return e, nil, err
	}
	pr := &github.PullRequest{Number: int(e.PrNumber.Int32)}
	pr.Head.SHA = e.HeadSha.String
	g, err := w.gateAndReview(ctx, e, f, ws, pr, "ESCALATE", e.DecisionReason.String, by)
	if err == nil {
		_ = w.comment(ctx, e.LinearIssueID, fmt.Sprintf("**Sentinela · execução #%d**: ESCALATE aprovado por %s (`sentinela job approve`). Gate: %s. O PR continua em draft; o merge é humano.", e.ID, by, g.Decision))
	}
	return e, g, err
}

// Rework é `sentinela job rework <id> "<nota>"`: devolve o job ao builder com a nota, na mesma
// branch e no mesmo PR, com as rodadas zeradas.
func (w *Worker) Rework(ctx context.Context, id int64, note string) (db.Execution, error) {
	if strings.TrimSpace(note) == "" {
		return db.Execution{}, errors.New("a nota do rework é obrigatória")
	}
	prev, err := w.Store.Q.GetExecution(ctx, id)
	if err != nil {
		return db.Execution{}, fmt.Errorf("job %d não existe", id)
	}
	if prev.Status == "queued" || prev.Status == "running" {
		return db.Execution{}, fmt.Errorf("job %d ainda está %s", id, prev.Status)
	}
	if !prev.Branch.Valid || !prev.PrNumber.Valid {
		return db.Execution{}, fmt.Errorf("job %d não tem PR para retomar: use sentinela job retry %d", id, id)
	}
	e, err := w.Store.Q.EnqueueExecution(ctx, db.EnqueueExecutionParams{
		FindingID: prev.FindingID, Kind: "rework", Repo: prev.Repo, IssueIdentifier: prev.IssueIdentifier,
		LinearIssueID: prev.LinearIssueID, ReworkNote: text(note), PreviousID: pgtype.Int8{Int64: prev.ID, Valid: true},
		Branch: prev.Branch, PrNumber: prev.PrNumber, PrUrl: prev.PrUrl, PrState: prev.PrState,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Execution{}, fmt.Errorf("já há um job ativo para este achado")
	}
	return e, err
}

// Retry é `sentinela job retry <id>`: um humano manda refazer do zero um job que falhou ou foi
// bloqueado (sem PR para retomar). Novo job do tipo fix, a partir da branch padrão; o worktree
// anterior é descartado pelo worker.
func (w *Worker) Retry(ctx context.Context, id int64) (db.Execution, error) {
	prev, err := w.Store.Q.GetExecution(ctx, id)
	if err != nil {
		return db.Execution{}, fmt.Errorf("job %d não existe", id)
	}
	if prev.Status != "failed" && prev.Decision.String != "blocked" {
		return db.Execution{}, fmt.Errorf("job %d está %s (%s): só um job que falhou ou foi bloqueado pode ser refeito", id, prev.Status, prev.Decision.String)
	}
	if prev.PrNumber.Valid {
		return db.Execution{}, fmt.Errorf("job %d já tem PR: use sentinela job rework", id)
	}
	e, err := w.Store.Q.EnqueueExecution(ctx, db.EnqueueExecutionParams{
		FindingID: prev.FindingID, Kind: "fix", Repo: prev.Repo, IssueIdentifier: prev.IssueIdentifier,
		LinearIssueID: prev.LinearIssueID, PreviousID: pgtype.Int8{Int64: prev.ID, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Execution{}, fmt.Errorf("já há um job ativo para este achado")
	}
	return e, err
}
