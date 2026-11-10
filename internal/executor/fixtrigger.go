package executor

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// fixTrigger guarda os membros dos times do Linear lidos neste ciclo do gatilho: cada time é lido
// uma vez por ciclo, qualquer que seja o número de issues (ADR 0042).
type fixTrigger struct {
	mu      sync.Mutex
	members map[string][]linear.Person
}

// resetMembers começa um ciclo novo: os membros são lidos de novo na primeira consulta.
func (w *Worker) resetMembers() {
	w.trigger.mu.Lock()
	w.trigger.members = nil
	w.trigger.mu.Unlock()
}

func (w *Worker) teamMembers(ctx context.Context, linearTeam string) ([]linear.Person, error) {
	w.trigger.mu.Lock()
	m, ok := w.trigger.members[linearTeam]
	w.trigger.mu.Unlock()
	if ok {
		return m, nil
	}
	m, err := w.Linear.TeamMembers(ctx, linearTeam)
	if err != nil {
		return nil, err
	}
	w.trigger.mu.Lock()
	if w.trigger.members == nil {
		w.trigger.members = map[string][]linear.Person{}
	}
	w.trigger.members[linearTeam] = m
	w.trigger.mu.Unlock()
	return m, nil
}

// permitted diz se a pessoa pode disparar a correção (e comentar instruções) no time.
func (w *Worker) permitted(ctx context.Context, allowed config.AllowedList, linearTeam string, p linear.Person) (bool, error) {
	if allowed.Anyone() {
		return true, nil
	}
	for _, u := range allowed.Users() {
		if p.Matches(u) {
			return true, nil
		}
	}
	if !allowed.TeamMembers() {
		return false, nil
	}
	members, err := w.teamMembers(ctx, linearTeam)
	if err != nil {
		return false, err
	}
	for _, m := range members {
		if m.Same(p) {
			return true, nil
		}
	}
	return false, nil
}

func (w *Worker) linearTeamOf(linearTeam string) string {
	if linearTeam != "" {
		return linearTeam
	}
	return w.Cfg.Defaults.Linear.Team
}

// authorizeTrigger confere quem aplicou a label de gatilho (ADR 0042). Recusado: não mexe na
// label nem na issue (tirar a label seria executar uma decisão humana); comenta uma vez por
// aplicação da label, registra a recusa e ignora até alguém autorizado aplicar a label de novo.
// No modo MCP, o histórico de labels não está disponível: o gatilho vale como antes (anyone), e o
// config validate avisa.
func (w *Worker) authorizeTrigger(ctx context.Context, findingID int64, issueID, identifier, team, linearTeam, label string) (bool, error) {
	allowed, src := w.Cfg.FixTriggerFor(team)
	if allowed.Anyone() {
		return true, nil
	}
	linearTeam = w.linearTeamOf(linearTeam)
	ev, found, err := w.Linear.TriggerActor(ctx, issueID, label)
	if errors.Is(err, linear.ErrUnsupported) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	who := "alguém que o histórico da issue não mostra"
	eventID := "sem-historico:" + issueID + ":" + label
	if found {
		ok, err := w.permitted(ctx, allowed, linearTeam, ev.Actor)
		if err != nil || ok {
			return ok, err
		}
		who, eventID = orDash(ev.Actor.Name, ev.Actor.Email), ev.ID
	}
	_, err = w.Store.Q.InsertTriggerRefusal(ctx, db.InsertTriggerRefusalParams{
		FindingID: findingID, LinearIssueID: issueID, EventID: eventID, Actor: who, Allowed: allowed.String(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // já recusado e comentado nesta aplicação da label
	}
	if err != nil {
		return false, err
	}
	w.log().Info("gatilho recusado", "issue", identifier, "por", who, "permitido", allowed.String())
	_ = w.comment(ctx, issueID, fmt.Sprintf("**Sentinela:** a label `%s` foi aplicada por %s, que não pode pedir a correção neste time (%s: %s, time %s do Linear). Não mexi na issue nem na label. Para pedir a correção, uma pessoa autorizada aplica `%s` de novo.",
		label, who, src, describeAllowed(allowed), linearTeam, label))
	return false, nil
}

func describeAllowed(a config.AllowedList) string {
	var parts []string
	if a.TeamMembers() {
		parts = append(parts, "membros do time dono")
	}
	if u := a.Users(); len(u) > 0 {
		parts = append(parts, strings.Join(u, ", "))
	}
	return strings.Join(parts, " ou ")
}

// instructionAuthors filtra os comentários que podem virar "Instruções da equipe": com
// fix_trigger restrito, só os de membros do time dono, dos usuários da lista e de quem aplicou a
// label de gatilho (ADR 0042). Devolve os comentários aceitos e os autores ignorados.
func (w *Worker) instructionAuthors(ctx context.Context, comments []linear.IssueComment, e *db.Execution, team, linearTeam string) ([]linear.IssueComment, []string, error) {
	allowed, _ := w.Cfg.FixTriggerFor(team)
	if allowed.Anyone() || w.Linear == nil {
		return comments, nil, nil
	}
	linearTeam = w.linearTeamOf(linearTeam)
	var applier *linear.Person
	if e.LinearIssueID != "" {
		ev, found, err := w.Linear.TriggerActor(ctx, e.LinearIssueID, w.Cfg.LabelsFor(team).Trigger)
		if err != nil && !errors.Is(err, linear.ErrUnsupported) {
			return nil, nil, err
		}
		if found {
			applier = &ev.Actor
		}
	}
	var out []linear.IssueComment
	ignored := map[string]bool{}
	for _, cm := range comments {
		if !cm.Human() {
			out = append(out, cm) // os não humanos já ficam de fora em TeamInstructions
			continue
		}
		p := linear.Person{ID: cm.User.ID, Email: cm.User.Email, Name: cm.User.Name}
		ok := applier != nil && applier.Same(p)
		if !ok {
			var err error
			if ok, err = w.permitted(ctx, allowed, linearTeam, p); err != nil {
				return nil, nil, err
			}
		}
		if ok {
			out = append(out, cm)
		} else {
			ignored[cm.User.Name] = true
		}
	}
	names := make([]string, 0, len(ignored))
	for n := range ignored {
		names = append(names, n)
	}
	sort.Strings(names)
	return out, names, nil
}
