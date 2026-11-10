// Package lineartest tem um Linear falso, em memória, que responde às operações GraphQL do
// Sentinela pelo nome da operação. Serve aos testes do publisher, do sync e de ponta a ponta.
package lineartest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

type Comment struct {
	ID        string // vazio: gerado pela posição
	Body      string
	User      string // vazio = comentário de bot
	CreatedAt time.Time
	App       bool // o usuário autor é um app
	Me        bool // o autor é a identidade do cliente (viewer)
}

// AppName é o nome do app que o fake usa quando AppActor está ligado.
const AppName = "Sentinela"

type Issue struct {
	ID, Identifier, URL, TeamID, ProjectID, StateID, AssigneeID string
	Title, Description                                          string
	LabelIDs                                                    []string
	Priority                                                    int
	UpdatedAt                                                   time.Time
	Comments                                                    []Comment
	CanceledBy                                                  string
	Related                                                     []string
	DuplicateOf                                                 string // id da issue original (relação "duplicate")
	// LabelsAddedBySentinela são as labels aplicadas pela API (o Sentinela), separadas das que um
	// humano aplicou com AddLabel: o teste confere que o gatilho nunca vem do Sentinela.
	LabelsAddedBySentinela []string
	// LabelEvents é o histórico de labels aplicadas por pessoas (quem e quando), para fix_trigger.
	LabelEvents []LabelEvent
	// CreatedAt (zero: UpdatedAt), Archived e Trashed servem à busca por marcador: arquivada e
	// lixeira ficam fora dela, como no Linear sem includeArchived.
	CreatedAt         time.Time
	Archived, Trashed bool
}

// LabelEvent é uma entrada do histórico: a label aplicada por uma pessoa.
type LabelEvent struct {
	ID, LabelID string
	UserID      string
	At          time.Time
}

// Person é um usuário do Linear falso (membro de time ou autor de uma ação).
type Person struct{ ID, Name, Email string }

type State struct{ ID, Name, Type string }

// Server é o Linear falso. Um time EX com estados, projetos, labels e um usuário.
type Server struct {
	mu       sync.Mutex
	Teams    map[string]string // key -> id
	States   map[string][]State
	Projects map[string]map[string]string // teamID -> nome -> id
	Labels   map[string]string            // nome -> id (do time EX)
	Users    map[string]string            // email -> id
	// People são as pessoas do workspace; Members, os ids dos membros de cada time (por id do time).
	People  []Person
	Members map[string][]string
	Issues  []*Issue
	Ops     []string // operações recebidas, em ordem
	// AppActor simula um token do app (actor=app): os comentários criados pela API saem com o
	// usuário do app (app, isMe). Desligado, saem como integração, como antes (ADR 0032).
	AppActor bool
	// Auth guarda o header Authorization de cada chamada, em ordem.
	Auth []string
	seq  int
}

func New() *Server {
	return &Server{
		People:  []Person{{"user-lead", "Lead", "lead@example.com"}, {"user-ana", "Ana", "ana@example.com"}, {"user-externo", "Externo", "externo@example.com"}},
		Members: map[string][]string{"team-ex": {"user-lead", "user-ana"}, "team-pay": {"user-lead"}},
		Teams:   map[string]string{"EX": "team-ex", "PAY": "team-pay"},
		States: map[string][]State{
			"team-ex":  {{"st-backlog", "Backlog", "backlog"}, {"st-todo", "Todo", "unstarted"}, {"st-review", "In Review", "started"}, {"st-done", "Done", "completed"}, {"st-canceled", "Canceled", "canceled"}, {"st-dup", "Duplicate", "duplicate"}},
			"team-pay": {{"pay-backlog", "Backlog", "backlog"}, {"pay-canceled", "Canceled", "canceled"}},
		},
		Projects: map[string]map[string]string{
			"team-ex":  {"core": "proj-core", "app-java-angular": "proj-app-java-angular", "plataforma": "proj-plat"},
			"team-pay": {"payments": "proj-pay"},
		},
		Labels: map[string]string{
			"sentinela": "lbl-sentinela", "claude-analysis": "lbl-claude",
			"sentinela-fix": "lbl-fix", "sentinela-done": "lbl-done", "sentinela-failed": "lbl-failed",
			"sentinela-pergunta": "lbl-pergunta", "sentinela-intencional": "lbl-intencional", "sentinela-nao-intencional": "lbl-nao-intencional",
		},
		Users: map[string]string{"lead@acme.com": "user-lead", "ana@acme.com": "user-ana", "joao@acme.com": "user-joao"},
	}
}

var opName = regexp.MustCompile(`(?:query|mutation)\s+(\w+)`)

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	m := opName.FindStringSubmatch(req.Query)
	if m == nil {
		http.Error(w, "operação sem nome", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Ops = append(s.Ops, m[1])
	s.Auth = append(s.Auth, r.Header.Get("Authorization"))
	data, err := s.handle(m[1], req.Variables)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"message": err.Error()}}})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func str(v any) string { s, _ := v.(string); return s }

func (s *Server) handle(op string, v map[string]any) (any, error) {
	switch op {
	case "SentinelaViewer":
		if s.AppActor {
			return map[string]any{"viewer": map[string]any{"id": "user-app", "name": AppName, "app": true}}, nil
		}
		return map[string]any{"viewer": map[string]any{"id": "user-lead", "name": "Lead", "app": false}}, nil
	case "SentinelaTeamRefs":
		id, ok := s.Teams[str(v["key"])]
		if !ok {
			return map[string]any{"teams": map[string]any{"nodes": []any{}}}, nil
		}
		var states []any
		for _, st := range s.States[id] {
			states = append(states, map[string]any{"id": st.ID, "name": st.Name})
		}
		return map[string]any{"teams": map[string]any{"nodes": []any{map[string]any{"id": id, "key": str(v["key"]), "states": map[string]any{"nodes": states}}}}}, nil
	case "SentinelaProject":
		nodes := []any{}
		if id, ok := s.Projects[str(v["team"])][str(v["name"])]; ok {
			nodes = append(nodes, map[string]any{"id": id})
		}
		return map[string]any{"team": map[string]any{"projects": map[string]any{"nodes": nodes}}}, nil
	case "SentinelaLabels":
		nodes := []any{}
		names, _ := v["names"].([]any)
		for _, n := range names {
			if id, ok := s.Labels[str(n)]; ok {
				nodes = append(nodes, map[string]any{"id": id, "name": str(n), "team": nil})
			}
		}
		return map[string]any{"issueLabels": map[string]any{"nodes": nodes}}, nil
	case "SentinelaUser":
		nodes := []any{}
		if id, ok := s.Users[str(v["email"])]; ok {
			nodes = append(nodes, map[string]any{"id": id})
		}
		return map[string]any{"users": map[string]any{"nodes": nodes}}, nil
	case "SentinelaMembers":
		nodes := []any{}
		for _, id := range s.Members[str(v["id"])] {
			nodes = append(nodes, s.person(id))
		}
		return map[string]any{"team": map[string]any{"members": map[string]any{"nodes": nodes}}}, nil
	case "SentinelaTriggerActor":
		is := s.byID(str(v["id"]))
		if is == nil {
			return map[string]any{"issue": nil}, nil
		}
		nodes := []any{}
		for _, ev := range is.LabelEvents {
			name := ""
			for n, id := range s.Labels {
				if id == ev.LabelID {
					name = n
				}
			}
			nodes = append(nodes, map[string]any{"id": ev.ID, "createdAt": ev.At.Format(time.RFC3339Nano), "actor": s.person(ev.UserID),
				"addedLabels": []any{map[string]any{"id": ev.LabelID, "name": name}}})
		}
		return map[string]any{"issue": map[string]any{"history": map[string]any{"nodes": nodes}}}, nil
	case "SentinelaFindIssue":
		nodes := []any{}
		for _, is := range s.Issues {
			if is.TeamID == str(v["team"]) && strings.Contains(is.Description, str(v["marker"])) && !is.Archived && !is.Trashed {
				view := s.view(is)
				created := is.CreatedAt
				if created.IsZero() {
					created = is.UpdatedAt
				}
				view["description"], view["createdAt"], view["archivedAt"], view["trashed"] = is.Description, created.Format(time.RFC3339Nano), nil, false
				view["team"], view["project"] = map[string]any{"id": is.TeamID}, nil
				if is.ProjectID != "" {
					view["project"] = map[string]any{"id": is.ProjectID}
				}
				nodes = append(nodes, view)
			}
		}
		return map[string]any{"issues": map[string]any{"nodes": nodes}}, nil
	case "SentinelaCreateIssue":
		in, _ := v["input"].(map[string]any)
		s.seq++
		is := &Issue{
			ID: fmt.Sprintf("issue-%d", s.seq), Identifier: fmt.Sprintf("EX-%d", s.seq),
			URL: fmt.Sprintf("https://linear.app/exemplo/issue/EX-%d", s.seq), TeamID: str(in["teamId"]),
			ProjectID: str(in["projectId"]), StateID: str(in["stateId"]), AssigneeID: str(in["assigneeId"]),
			Title: str(in["title"]), Description: str(in["description"]), UpdatedAt: time.Now(),
		}
		if p, ok := in["priority"].(float64); ok {
			is.Priority = int(p)
		}
		if ls, ok := in["labelIds"].([]any); ok {
			for _, l := range ls {
				is.LabelIDs = append(is.LabelIDs, str(l))
			}
		}
		if is.StateID == "" {
			is.StateID = s.States[is.TeamID][0].ID
		}
		s.Issues = append(s.Issues, is)
		return map[string]any{"issueCreate": map[string]any{"success": true, "issue": s.view(is)}}, nil
	case "SentinelaRelate":
		in, _ := v["input"].(map[string]any)
		if is := s.byID(str(in["issueId"])); is != nil {
			is.Related = append(is.Related, str(in["relatedIssueId"]))
		}
		return map[string]any{"issueRelationCreate": map[string]any{"success": true}}, nil
	case "SentinelaIssues":
		nodes := []any{}
		ids, _ := v["ids"].([]any)
		for _, id := range ids {
			if is := s.byID(str(id)); is != nil {
				nodes = append(nodes, s.view(is))
			}
		}
		return map[string]any{"issues": map[string]any{"nodes": nodes}}, nil
	case "SentinelaClosure":
		is := s.byID(str(v["id"]))
		if is == nil {
			return map[string]any{"issue": nil}, nil
		}
		view := s.view(is)
		var comments []any
		for _, c := range is.Comments {
			cm := map[string]any{"body": c.Body, "createdAt": c.CreatedAt.Format(time.RFC3339Nano)}
			commentAuthor(cm, c)
			comments = append(comments, cm)
		}
		view["comments"] = map[string]any{"nodes": comments}
		hist := []any{}
		switch {
		case is.CanceledBy != "" && is.DuplicateOf != "":
			// Como no Linear real: a marcação de Duplicate vem sem toState.
			hist = append(hist, map[string]any{"createdAt": is.UpdatedAt.Format(time.RFC3339Nano), "actor": map[string]any{"name": is.CanceledBy}, "toState": nil})
		case is.CanceledBy != "":
			hist = append(hist, map[string]any{"createdAt": is.UpdatedAt.Format(time.RFC3339Nano), "actor": map[string]any{"name": is.CanceledBy}, "toState": map[string]any{"type": s.state(is).Type}})
		}
		view["history"] = map[string]any{"nodes": hist}
		return map[string]any{"issue": view}, nil
	case "SentinelaDetail":
		is := s.byID(str(v["id"]))
		if is == nil {
			return map[string]any{"issue": nil}, nil
		}
		view := s.view(is)
		view["title"], view["description"] = is.Title, is.Description
		comments := []any{}
		for i, c := range is.Comments {
			id := c.ID
			if id == "" {
				id = fmt.Sprintf("%s-c%d", is.ID, i+1)
			}
			cm := map[string]any{"id": id, "body": c.Body, "createdAt": c.CreatedAt.Format(time.RFC3339Nano)}
			commentAuthor(cm, c)
			comments = append(comments, cm)
		}
		view["comments"] = map[string]any{"nodes": comments}
		return map[string]any{"issue": view}, nil
	case "SentinelaDuplicateOf":
		is := s.byID(str(v["id"]))
		if is == nil {
			return map[string]any{"issue": nil}, nil
		}
		nodes := []any{}
		if orig := s.byID(is.DuplicateOf); orig != nil {
			nodes = append(nodes, map[string]any{"type": "duplicate", "relatedIssue": s.view(orig)})
		}
		return map[string]any{"issue": map[string]any{"relations": map[string]any{"nodes": nodes}}}, nil
	case "SentinelaSetState":
		is := s.byID(str(v["id"]))
		if is == nil {
			return nil, fmt.Errorf("issue %s não existe", str(v["id"]))
		}
		in, _ := v["input"].(map[string]any)
		is.StateID, is.UpdatedAt = str(in["stateId"]), time.Now()
		s.Ops = append(s.Ops, "state:"+is.Identifier+":"+s.state(is).Name)
		return map[string]any{"issueUpdate": map[string]any{"success": true}}, nil
	case "SentinelaUpdateIssue":
		is := s.byID(str(v["id"]))
		if is == nil {
			return nil, fmt.Errorf("issue %s não existe", str(v["id"]))
		}
		in, _ := v["input"].(map[string]any)
		if t := str(in["title"]); t != "" {
			is.Title = t
		}
		if d := str(in["description"]); d != "" {
			is.Description = d
		}
		if add, ok := in["addedLabelIds"].([]any); ok {
			for _, l := range add {
				is.LabelIDs = append(is.LabelIDs, str(l))
				is.LabelsAddedBySentinela = append(is.LabelsAddedBySentinela, str(l))
			}
		}
		if rm, ok := in["removedLabelIds"].([]any); ok {
			for _, l := range rm {
				is.LabelIDs = slicesDelete(is.LabelIDs, str(l))
			}
		}
		is.UpdatedAt = time.Now()
		return map[string]any{"issueUpdate": map[string]any{"success": true}}, nil
	case "SentinelaComment":
		in, _ := v["input"].(map[string]any)
		is := s.byID(str(in["issueId"]))
		if is == nil {
			return nil, fmt.Errorf("issue %s não existe", str(in["issueId"]))
		}
		cm := Comment{Body: str(in["body"]), CreatedAt: time.Now()}
		if s.AppActor {
			cm.User, cm.App, cm.Me = AppName, true, true
		}
		is.Comments = append(is.Comments, cm)
		return map[string]any{"commentCreate": map[string]any{"success": true}}, nil
	case "SentinelaAddLabel":
		is := s.byID(str(v["id"]))
		if is == nil {
			return nil, fmt.Errorf("issue %s não existe", str(v["id"]))
		}
		is.LabelIDs = append(is.LabelIDs, str(v["labelId"]))
		is.LabelsAddedBySentinela = append(is.LabelsAddedBySentinela, str(v["labelId"]))
		return map[string]any{"issueAddLabel": map[string]any{"success": true}}, nil
	}
	return nil, fmt.Errorf("operação não suportada pelo fake: %s", op)
}

func (s *Server) byID(id string) *Issue {
	for _, is := range s.Issues {
		if is.ID == id {
			return is
		}
	}
	return nil
}

func (s *Server) state(is *Issue) State {
	for _, st := range s.States[is.TeamID] {
		if st.ID == is.StateID {
			return st
		}
	}
	return State{}
}

func (s *Server) view(is *Issue) map[string]any {
	st := s.state(is)
	labels := []any{}
	for _, id := range is.LabelIDs {
		for name, lid := range s.Labels {
			if lid == id {
				labels = append(labels, map[string]any{"id": id, "name": name})
			}
		}
	}
	return map[string]any{
		"id": is.ID, "identifier": is.Identifier, "url": is.URL, "updatedAt": is.UpdatedAt.Format(time.RFC3339Nano),
		"state": map[string]any{"name": st.Name, "type": st.Type}, "labels": map[string]any{"nodes": labels},
	}
}

// AddLabel simula uma pessoa (a "Lead", membro do time EX) aplicando uma label na issue (ex.:
// sentinela-fix).
func (s *Server) AddLabel(identifier, name string) { s.AddLabelBy(identifier, name, "user-lead") }

// AddLabelBy simula a pessoa userID aplicando a label: entra no histórico, para fix_trigger.
func (s *Server) AddLabelBy(identifier, name, userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, is := range s.Issues {
		if is.Identifier == identifier {
			is.LabelIDs = append(is.LabelIDs, s.Labels[name])
			is.UpdatedAt = time.Now()
			s.seq++
			is.LabelEvents = append(is.LabelEvents, LabelEvent{ID: fmt.Sprintf("hist-%d", s.seq), LabelID: s.Labels[name], UserID: userID, At: is.UpdatedAt})
		}
	}
}

func (s *Server) person(id string) map[string]any {
	for _, p := range s.People {
		if p.ID == id {
			return map[string]any{"id": p.ID, "name": p.Name, "email": p.Email}
		}
	}
	return map[string]any{"id": id, "name": id, "email": id + "@example.com"}
}

// RemoveLabel simula uma pessoa tirando uma label da issue.
func (s *Server) RemoveLabel(identifier, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, is := range s.Issues {
		if is.Identifier == identifier {
			is.LabelIDs = slices.DeleteFunc(is.LabelIDs, func(id string) bool { return id == s.Labels[name] })
			is.UpdatedAt = time.Now()
		}
	}
}

// Move simula uma pessoa mudando a issue de estado (pelo nome do estado no time).
func (s *Server) Move(identifier, stateName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, is := range s.Issues {
		if is.Identifier != identifier {
			continue
		}
		for _, st := range s.States[is.TeamID] {
			if st.Name == stateName {
				is.StateID = st.ID
				is.UpdatedAt = time.Now()
			}
		}
	}
}

// Comment simula uma pessoa comentando na issue.
func (s *Server) Comment(identifier, by, body string) {
	s.CommentAt(identifier, by, body, time.Now())
}

// CommentAt é um comentário com data escolhida (by vazio = comentário de integração).
func (s *Server) CommentAt(identifier, by, body string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, is := range s.Issues {
		if is.Identifier == identifier {
			is.Comments = append(is.Comments, Comment{Body: body, User: by, CreatedAt: at})
			is.UpdatedAt = time.Now()
		}
	}
}

// MarkDuplicate simula uma pessoa marcando a issue como Duplicate de outra.
func (s *Server) MarkDuplicate(identifier, original, by string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var orig *Issue
	for _, is := range s.Issues {
		if is.Identifier == original {
			orig = is
		}
	}
	for _, is := range s.Issues {
		if is.Identifier != identifier || orig == nil {
			continue
		}
		for _, st := range s.States[is.TeamID] {
			if st.Type == "duplicate" {
				is.StateID = st.ID
			}
		}
		is.DuplicateOf, is.CanceledBy, is.UpdatedAt = orig.ID, by, time.Now()
	}
}

func slicesDelete(xs []string, x string) []string {
	out := xs[:0]
	for _, v := range xs {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}

// Comments devolve os comentários de uma issue.
func (s *Server) CommentsOf(identifier string) []Comment {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, is := range s.Issues {
		if is.Identifier == identifier {
			return append([]Comment{}, is.Comments...)
		}
	}
	return nil
}

// Cancel simula uma pessoa cancelando a issue com um comentário de motivo.
func (s *Server) Cancel(identifier, by, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, is := range s.Issues {
		if is.Identifier != identifier {
			continue
		}
		for _, st := range s.States[is.TeamID] {
			if st.Type == "canceled" {
				is.StateID = st.ID
			}
		}
		is.CanceledBy = by
		is.UpdatedAt = time.Now()
		if reason != "" {
			is.Comments = append(is.Comments, Comment{Body: reason, User: by, CreatedAt: time.Now()})
		}
	}
}

// Snapshot devolve uma cópia das issues criadas.
func (s *Server) Snapshot() []Issue {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Issue, 0, len(s.Issues))
	for _, is := range s.Issues {
		out = append(out, *is)
	}
	return out
}

// Count conta as operações com o nome dado.
func (s *Server) Count(op string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, o := range s.Ops {
		if o == op {
			n++
		}
	}
	return n
}

// commentAuthor preenche user e botActor como o Linear devolve: comentário de usuário (pessoa ou
// app) tem user; de integração, só botActor.
func commentAuthor(cm map[string]any, c Comment) {
	if c.User == "" {
		cm["user"], cm["botActor"] = nil, map[string]any{"name": "bot"}
		return
	}
	cm["user"], cm["botActor"] = map[string]any{"name": c.User, "app": c.App, "isMe": c.Me}, nil
}

// HasLabelID diz se a issue tem a label (pelo id).
func (is Issue) HasLabelID(id string) bool {
	for _, l := range is.LabelIDs {
		if l == id {
			return true
		}
	}
	return false
}
