package linear

// Operações de issue da Fase 3: publicar achados e ler o estado de volta.
// Tipos e campos conferidos por introspecção da API em 2026-10-10 (IssueCreateInput,
// IssueRelationCreateInput com IssueRelationType related, Issue.history com toState e actor,
// Comment com user e botActor) e em https://linear.app/developers/graphql.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// Issue é a parte de uma issue do Linear que o Sentinela usa.
type Issue struct {
	ID         string    `json:"id"`
	Identifier string    `json:"identifier"`
	URL        string    `json:"url"`
	UpdatedAt  time.Time `json:"updatedAt"`
	State      struct {
		Name string `json:"name"`
		Type string `json:"type"` // triage | backlog | unstarted | started | completed | canceled | duplicate
	} `json:"state"`
	Labels struct {
		Nodes []Label `json:"nodes"`
	} `json:"labels"`
}

// Label é uma label do Linear.
type Label struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// HasLabel diz se a issue tem a label (pelo nome, sem diferenciar maiúsculas).
func (i Issue) HasLabel(name string) bool {
	for _, l := range i.Labels.Nodes {
		if strings.EqualFold(l.Name, name) {
			return true
		}
	}
	return false
}

const issueFields = `id identifier url updatedAt state { name type } labels { nodes { id name } }`

// TeamRefs tem os IDs de um time necessários para criar issues.
type TeamRefs struct {
	ID     string
	Key    string
	States map[string]string // nome do estado -> id
}

// ErrNotFound indica que o time, projeto, estado, label ou usuário não existe no Linear.
var ErrNotFound = errors.New("não encontrado no Linear")

func (c *Client) TeamRefs(ctx context.Context, key string) (*TeamRefs, error) {
	var data struct {
		Teams struct {
			Nodes []struct {
				ID     string `json:"id"`
				Key    string `json:"key"`
				States struct {
					Nodes []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"nodes"`
				} `json:"states"`
			} `json:"nodes"`
		} `json:"teams"`
	}
	const q = `query SentinelaTeamRefs($key: String!) {
  teams(filter: { key: { eq: $key } }) { nodes { id key states(first: 100) { nodes { id name } } } }
}`
	if err := c.query(ctx, q, map[string]any{"key": key}, &data); err != nil {
		return nil, err
	}
	if len(data.Teams.Nodes) == 0 {
		return nil, fmt.Errorf("%w: time %s", ErrNotFound, key)
	}
	t := data.Teams.Nodes[0]
	refs := &TeamRefs{ID: t.ID, Key: t.Key, States: map[string]string{}}
	for _, s := range t.States.Nodes {
		refs.States[s.Name] = s.ID
	}
	return refs, nil
}

// ProjectID procura um projeto do time pelo nome exato.
func (c *Client) ProjectID(ctx context.Context, teamID, name string) (string, error) {
	var data struct {
		Team struct {
			Projects struct {
				Nodes []struct {
					ID string `json:"id"`
				} `json:"nodes"`
			} `json:"projects"`
		} `json:"team"`
	}
	const q = `query SentinelaProject($team: String!, $name: String!) {
  team(id: $team) { projects(first: 2, filter: { name: { eq: $name } }) { nodes { id } } }
}`
	if err := c.query(ctx, q, map[string]any{"team": teamID, "name": name}, &data); err != nil {
		return "", err
	}
	if len(data.Team.Projects.Nodes) == 0 {
		return "", fmt.Errorf("%w: projeto %q", ErrNotFound, name)
	}
	return data.Team.Projects.Nodes[0].ID, nil
}

// LabelIDs resolve labels pelo nome, preferindo as do time às do workspace. Labels de outro
// sistema (claude-*) nunca devem chegar aqui; o chamador filtra pela config.
func (c *Client) LabelIDs(ctx context.Context, teamID string, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	var data struct {
		IssueLabels struct {
			Nodes []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				Team *struct {
					ID string `json:"id"`
				} `json:"team"`
			} `json:"nodes"`
		} `json:"issueLabels"`
	}
	const q = `query SentinelaLabels($names: [String!]!) {
  issueLabels(first: 100, filter: { name: { in: $names } }) { nodes { id name team { id } } }
}`
	if err := c.query(ctx, q, map[string]any{"names": names}, &data); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(names))
	for _, name := range names {
		id := ""
		for _, l := range data.IssueLabels.Nodes {
			if l.Name != name {
				continue
			}
			if l.Team != nil && l.Team.ID == teamID {
				id = l.ID
				break
			}
			if l.Team == nil && id == "" {
				id = l.ID
			}
		}
		if id == "" {
			return nil, fmt.Errorf("%w: label %q", ErrNotFound, name)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// UserIDByEmail resolve o responsável de uma issue.
func (c *Client) UserIDByEmail(ctx context.Context, email string) (string, error) {
	var data struct {
		Users struct {
			Nodes []struct {
				ID string `json:"id"`
			} `json:"nodes"`
		} `json:"users"`
	}
	const q = `query SentinelaUser($email: String!) { users(filter: { email: { eq: $email } }) { nodes { id } } }`
	if err := c.query(ctx, q, map[string]any{"email": email}, &data); err != nil {
		return "", err
	}
	if len(data.Users.Nodes) == 0 {
		return "", fmt.Errorf("%w: usuário %s", ErrNotFound, email)
	}
	return data.Users.Nodes[0].ID, nil
}

// FindIssuesByMarker lista as issues não arquivadas do time cuja descrição contém marker (o
// marcador base, sem instalação), com o que pickMarked confere: descrição, time, projeto, criação
// e lixeira. Fonte dos campos archivedAt, trashed, project e createdAt: introspecção do tipo Issue
// da API GraphQL do Linear (https://api.linear.app/graphql), conferida em 2026-10-14.
func (c *Client) FindIssuesByMarker(ctx context.Context, teamID, marker string) ([]MarkedIssue, error) {
	var data struct {
		Issues struct {
			Nodes []struct {
				Issue
				Description string     `json:"description"`
				CreatedAt   time.Time  `json:"createdAt"`
				ArchivedAt  *time.Time `json:"archivedAt"`
				Trashed     *bool      `json:"trashed"`
				Team        struct {
					ID string `json:"id"`
				} `json:"team"`
				Project *struct {
					ID string `json:"id"`
				} `json:"project"`
			} `json:"nodes"`
		} `json:"issues"`
	}
	// Sem includeArchived: arquivadas (e as da lixeira, que o Linear também arquiva) ficam de fora.
	q := `query SentinelaFindIssue($team: ID!, $marker: String!) {
  issues(first: 50, filter: { team: { id: { eq: $team } }, description: { contains: $marker } }) { nodes { ` + issueFields + ` description createdAt archivedAt trashed team { id } project { id } } }
}`
	if err := c.query(ctx, q, map[string]any{"team": teamID, "marker": marker}, &data); err != nil {
		return nil, err
	}
	out := make([]MarkedIssue, 0, len(data.Issues.Nodes))
	for _, n := range data.Issues.Nodes {
		m := MarkedIssue{Issue: n.Issue, Description: n.Description, TeamID: n.Team.ID, CreatedAt: n.CreatedAt,
			Archived: n.ArchivedAt != nil, Trashed: n.Trashed != nil && *n.Trashed}
		if n.Project != nil {
			m.ProjectID = n.Project.ID
		}
		out = append(out, m)
	}
	return out, nil
}

// IssueInput é o subconjunto de IssueCreateInput que o publisher usa.
type IssueInput struct {
	TeamID      string   `json:"teamId"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	ProjectID   string   `json:"projectId,omitempty"`
	StateID     string   `json:"stateId,omitempty"`
	AssigneeID  string   `json:"assigneeId,omitempty"`
	LabelIDs    []string `json:"labelIds,omitempty"`
	Priority    int      `json:"priority,omitempty"` // 1 urgente, 2 alta, 3 média, 4 baixa
}

func (c *Client) CreateIssue(ctx context.Context, in IssueInput) (Issue, error) {
	var data struct {
		IssueCreate struct {
			Success bool   `json:"success"`
			Issue   *Issue `json:"issue"`
		} `json:"issueCreate"`
	}
	q := `mutation SentinelaCreateIssue($input: IssueCreateInput!) {
  issueCreate(input: $input) { success issue { ` + issueFields + ` } }
}`
	if err := c.query(ctx, q, map[string]any{"input": in}, &data); err != nil {
		return Issue{}, err
	}
	if !data.IssueCreate.Success || data.IssueCreate.Issue == nil {
		return Issue{}, errors.New("linear: issueCreate sem sucesso")
	}
	return *data.IssueCreate.Issue, nil
}

// RelateIssues liga duas issues com a relação "related".
func (c *Client) RelateIssues(ctx context.Context, issueID, relatedID string) error {
	var data struct {
		IssueRelationCreate struct {
			Success bool `json:"success"`
		} `json:"issueRelationCreate"`
	}
	const q = `mutation SentinelaRelate($input: IssueRelationCreateInput!) {
  issueRelationCreate(input: $input) { success }
}`
	in := map[string]any{"issueId": issueID, "relatedIssueId": relatedID, "type": "related"}
	if err := c.query(ctx, q, map[string]any{"input": in}, &data); err != nil {
		return err
	}
	if !data.IssueRelationCreate.Success {
		return errors.New("linear: issueRelationCreate sem sucesso")
	}
	return nil
}

// Issues lê o estado atual de várias issues, em lotes de 50 IDs.
func (c *Client) Issues(ctx context.Context, ids []string) ([]Issue, error) {
	q := `query SentinelaIssues($ids: [ID!]!) {
  issues(first: 50, includeArchived: true, filter: { id: { in: $ids } }) { nodes { ` + issueFields + ` } }
}`
	var out []Issue
	for chunk := range slices.Chunk(ids, 50) {
		var data struct {
			Issues struct {
				Nodes []Issue `json:"nodes"`
			} `json:"issues"`
		}
		if err := c.query(ctx, q, map[string]any{"ids": chunk}, &data); err != nil {
			return nil, err
		}
		out = append(out, data.Issues.Nodes...)
	}
	return out, nil
}

// Closure explica quem fechou uma issue e por quê.
type Closure struct {
	Issue  Issue
	Actor  string // quem moveu para o estado atual, se o histórico disser
	Reason string // último comentário humano; vazio se não houver
}

// Closure lê o motivo de uma issue fechada: o último comentário de uma pessoa (não de bot)
// e quem fez a última mudança para o tipo de estado atual.
func (c *Client) Closure(ctx context.Context, id string) (Closure, error) {
	type actor struct {
		Name string `json:"name"`
	}
	var data struct {
		Issue *struct {
			Issue
			Comments struct {
				Nodes []IssueComment `json:"nodes"`
			} `json:"comments"`
			History struct {
				Nodes []struct {
					CreatedAt time.Time `json:"createdAt"`
					Actor     *actor    `json:"actor"`
					ToState   *struct {
						Type string `json:"type"`
					} `json:"toState"`
				} `json:"nodes"`
			} `json:"history"`
		} `json:"issue"`
	}
	q := `query SentinelaClosure($id: String!) {
  issue(id: $id) {
    ` + issueFields + `
    comments(first: 50) { nodes { body createdAt user { id name email app isMe } botActor { name } } }
    history(first: 50) { nodes { createdAt actor { name } toState { type } } }
  }
}`
	if err := c.query(ctx, q, map[string]any{"id": id}, &data); err != nil {
		return Closure{}, err
	}
	if data.Issue == nil {
		return Closure{}, fmt.Errorf("%w: issue %s", ErrNotFound, id)
	}
	out := Closure{Issue: data.Issue.Issue}
	comments := data.Issue.Comments.Nodes
	sort.Slice(comments, func(i, j int) bool { return comments[i].CreatedAt.Before(comments[j].CreatedAt) })
	c.markOwn(comments)
	for _, cm := range comments {
		// Comentário do Sentinela (pelo autor, ADR 0032) ou de integração não é o motivo de uma
		// decisão humana.
		if cm.Human() {
			out.Reason = strings.TrimSpace(cm.Body)
		}
	}
	var last, lastAny time.Time
	fallback := ""
	for _, h := range data.Issue.History.Nodes {
		if h.Actor == nil {
			continue
		}
		if h.ToState != nil && h.ToState.Type == out.Issue.State.Type && h.CreatedAt.After(last) {
			out.Actor, last = h.Actor.Name, h.CreatedAt
		}
		if h.CreatedAt.After(lastAny) {
			fallback, lastAny = h.Actor.Name, h.CreatedAt
		}
	}
	// Marcar como Duplicate gera uma entrada de histórico sem toState (visto na EX-8): sem a
	// entrada de mudança de estado, vale a pessoa da alteração mais recente.
	if out.Actor == "" {
		out.Actor = fallback
	}
	return out, nil
}

// IssueDetail é o conteúdo da issue que o job recebe (issue.md), com os comentários.
type IssueDetail struct {
	Issue
	Title       string `json:"title"`
	Description string `json:"description"`
	Comments    struct {
		Nodes []IssueComment `json:"nodes"`
	} `json:"comments"`
}

// IssueComment é um comentário de issue. User é o usuário autor (pessoa ou app; nil em
// comentário de integração); BotActor, a integração que comentou. BySentinela é marcado pelo
// cliente ao ler a issue (ADR 0032).
type IssueComment struct {
	ID          string    `json:"id"`
	Body        string    `json:"body"`
	CreatedAt   time.Time `json:"createdAt"`
	User        *Actor    `json:"user"`
	BotActor    *Actor    `json:"botActor"`
	BySentinela bool      `json:"bySentinela,omitempty"`
}

// Actor é o autor de um comentário ou de uma mudança. App: o usuário é um app (o Sentinela ou
// outra automação); IsMe: é a identidade com que o cliente está autenticado.
type Actor struct {
	ID    string `json:"id,omitempty"`
	Email string `json:"email,omitempty"`
	Name  string `json:"name"`
	App   bool   `json:"app,omitempty"`
	IsMe  bool   `json:"isMe,omitempty"`
}

// Human diz se o comentário é de uma pessoa: tem usuário que não é app, não é de integração e
// não é do Sentinela.
func (cm IssueComment) Human() bool {
	return cm.User != nil && !cm.User.App && cm.BotActor == nil && strings.TrimSpace(cm.Body) != "" && !cm.BySentinela
}

// markOwn marca os comentários do Sentinela. Com o app (actor=app), pelo autor: o usuário
// autenticado (isMe), que é o app. O prefixo "**Sentinela" vira só texto, exceto em comentários
// anteriores à autorização do app, da época da chave pessoal. Com a chave pessoal, o autor é a
// pessoa dona dela, e só o prefixo separa o Sentinela.
func (c *Client) markOwn(comments []IssueComment) {
	for i := range comments {
		comments[i].BySentinela = c.ownComment(comments[i])
	}
}

func (c *Client) ownComment(cm IssueComment) bool {
	if !c.AppActor() {
		return IsSentinelaComment(cm.Body)
	}
	if cm.User != nil && cm.User.IsMe {
		return true
	}
	return IsSentinelaComment(cm.Body) && cm.CreatedAt.Before(c.legacyPrefixUntil)
}

// Detail lê título, descrição e comentários de uma issue. Campos de Comment (id, body,
// createdAt, user, botActor) conferidos por introspecção em 2026-10-10.
func (c *Client) Detail(ctx context.Context, id string) (IssueDetail, error) {
	q := `query SentinelaDetail($id: String!) { issue(id: $id) { ` + issueFields + ` title description
    comments(first: 100) { nodes { id body createdAt user { id name email app isMe } botActor { name } } } } }`
	var data struct {
		Issue *IssueDetail `json:"issue"`
	}
	if err := c.query(ctx, q, map[string]any{"id": id}, &data); err != nil {
		return IssueDetail{}, err
	}
	if data.Issue == nil {
		return IssueDetail{}, fmt.Errorf("%w: issue %s", ErrNotFound, id)
	}
	c.markOwn(data.Issue.Comments.Nodes)
	return *data.Issue, nil
}

// Comment publica um comentário do Sentinela na issue.
// Mutation commentCreate(input: CommentCreateInput{issueId, body}) conferida por introspecção em 2026-10-10.
func (c *Client) Comment(ctx context.Context, issueID, body string) error {
	const q = `mutation SentinelaComment($input: CommentCreateInput!) { commentCreate(input: $input) { success } }`
	var data struct {
		CommentCreate struct {
			Success bool `json:"success"`
		} `json:"commentCreate"`
	}
	if err := c.query(ctx, q, map[string]any{"input": map[string]string{"issueId": issueID, "body": body}}, &data); err != nil {
		return err
	}
	if !data.CommentCreate.Success {
		return fmt.Errorf("linear: commentCreate sem sucesso em %s", issueID)
	}
	return nil
}

// AddLabel acrescenta uma label à issue (issueAddLabel(id, labelId), conferida por introspecção).
// O Sentinela só aplica as labels de resultado (sentinela-done, sentinela-failed); a label de
// gatilho (sentinela-fix) é decisão humana e nunca é aplicada por código (princípio 6).
func (c *Client) AddLabel(ctx context.Context, issueID, labelID string) error {
	const q = `mutation SentinelaAddLabel($id: String!, $labelId: String!) { issueAddLabel(id: $id, labelId: $labelId) { success } }`
	var data struct {
		IssueAddLabel struct {
			Success bool `json:"success"`
		} `json:"issueAddLabel"`
	}
	if err := c.query(ctx, q, map[string]any{"id": issueID, "labelId": labelID}, &data); err != nil {
		return err
	}
	if !data.IssueAddLabel.Success {
		return fmt.Errorf("linear: issueAddLabel sem sucesso em %s", issueID)
	}
	return nil
}

// DuplicateOf devolve a issue original de uma issue marcada como Duplicate (relação "duplicate"
// em issue.relations, conferida por introspecção em 2026-10-10); nil se não houver.
func (c *Client) DuplicateOf(ctx context.Context, id string) (*Issue, error) {
	const q = `query SentinelaDuplicateOf($id: String!) {
  issue(id: $id) { relations(first: 50) { nodes { type relatedIssue { id identifier url } } } }
}`
	var data struct {
		Issue *struct {
			Relations struct {
				Nodes []struct {
					Type         string `json:"type"`
					RelatedIssue *Issue `json:"relatedIssue"`
				} `json:"nodes"`
			} `json:"relations"`
		} `json:"issue"`
	}
	if err := c.query(ctx, q, map[string]any{"id": id}, &data); err != nil {
		return nil, err
	}
	if data.Issue == nil {
		return nil, nil
	}
	for _, r := range data.Issue.Relations.Nodes {
		if r.Type == "duplicate" && r.RelatedIssue != nil {
			return r.RelatedIssue, nil
		}
	}
	return nil, nil
}

// SetState move a issue para um estado (issueUpdate com stateId, conferido por introspecção em
// 2026-10-10). Só o worker usa, para registrar que o PR foi aberto (ADR 0027).
func (c *Client) SetState(ctx context.Context, id, stateID string) error {
	const q = `mutation SentinelaSetState($id: String!, $input: IssueUpdateInput!) { issueUpdate(id: $id, input: $input) { success } }`
	var data struct {
		IssueUpdate struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	}
	if err := c.query(ctx, q, map[string]any{"id": id, "input": map[string]string{"stateId": stateID}}, &data); err != nil {
		return err
	}
	if !data.IssueUpdate.Success {
		return fmt.Errorf("linear: issueUpdate (estado) sem sucesso em %s", id)
	}
	return nil
}

// UpdateIssue troca título e descrição e mexe em labels (achado com premissa respondido).
func (c *Client) UpdateIssue(ctx context.Context, id, title, description string, addLabels, removeLabels []string) error {
	const q = `mutation SentinelaUpdateIssue($id: String!, $input: IssueUpdateInput!) { issueUpdate(id: $id, input: $input) { success } }`
	in := map[string]any{}
	if title != "" {
		in["title"] = title
	}
	if description != "" {
		in["description"] = description
	}
	if len(addLabels) > 0 {
		in["addedLabelIds"] = addLabels
	}
	if len(removeLabels) > 0 {
		in["removedLabelIds"] = removeLabels
	}
	var data struct {
		IssueUpdate struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	}
	if err := c.query(ctx, q, map[string]any{"id": id, "input": in}, &data); err != nil {
		return err
	}
	if !data.IssueUpdate.Success {
		return fmt.Errorf("linear: issueUpdate sem sucesso em %s", id)
	}
	return nil
}

// IsSentinelaComment diz se o texto tem o prefixo dos comentários do Sentinela. Com o app, o
// autor é que identifica o Sentinela (ADR 0032); o prefixo vale para a chave pessoal e para
// comentários antigos.
func IsSentinelaComment(body string) bool {
	return strings.HasPrefix(strings.TrimSpace(body), "**Sentinela")
}

// Viewer é a identidade com que o cliente está autenticado.
type Viewer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	App  bool   `json:"app"`
}

// Viewer lê a identidade autenticada: com o app (actor=app), o usuário do app (app: true).
func (c *Client) Viewer(ctx context.Context) (Viewer, error) {
	var data struct {
		Viewer Viewer `json:"viewer"`
	}
	if err := c.query(ctx, `query SentinelaViewer { viewer { id name app } }`, nil, &data); err != nil {
		return Viewer{}, err
	}
	return data.Viewer, nil
}

// Person é um usuário do Linear: membro de time ou autor de uma ação (ADR 0042).
type Person struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Same diz se as duas pessoas são a mesma: pelo id, senão pelo e-mail, senão pelo nome (o MCP só
// dá o nome do autor de um comentário).
func (p Person) Same(o Person) bool {
	switch {
	case p.ID != "" && o.ID != "":
		return p.ID == o.ID
	case p.Email != "" && o.Email != "":
		return strings.EqualFold(p.Email, o.Email)
	}
	return p.Name != "" && strings.EqualFold(p.Name, o.Name)
}

// Matches diz se a pessoa é a citada por e-mail ou nome numa lista da config.
func (p Person) Matches(ref string) bool {
	ref = strings.TrimSpace(ref)
	return ref != "" && (strings.EqualFold(ref, p.Email) || strings.EqualFold(ref, p.Name) || ref == p.ID)
}

// TriggerEvent é a entrada do histórico em que a label de gatilho foi aplicada.
type TriggerEvent struct {
	ID    string
	Actor Person
	At    time.Time
}

// ErrUnsupported: o tracker não sabe responder (o MCP do Linear não expõe o histórico de labels).
var ErrUnsupported = errors.New("o tracker não dá essa informação")

// TeamMembers lê os membros de um time do Linear.
func (c *Client) TeamMembers(ctx context.Context, teamKey string) ([]Person, error) {
	refs, err := c.TeamRefs(ctx, teamKey)
	if err != nil {
		return nil, err
	}
	var data struct {
		Team *struct {
			Members struct {
				Nodes []Person `json:"nodes"`
			} `json:"members"`
		} `json:"team"`
	}
	const q = `query SentinelaMembers($id: String!) { team(id: $id) { members(first: 250) { nodes { id name email } } } }`
	if err := c.query(ctx, q, map[string]any{"id": refs.ID}, &data); err != nil {
		return nil, err
	}
	if data.Team == nil {
		return nil, fmt.Errorf("%w: time %s", ErrNotFound, teamKey)
	}
	return data.Team.Members.Nodes, nil
}

// TriggerActor acha no histórico da issue a última vez em que a label foi aplicada, e por quem.
// Campos de IssueHistory (id, createdAt, actor, addedLabels) conferidos por introspecção em
// 2026-10-11. found=false: o histórico não mostra a label sendo aplicada.
func (c *Client) TriggerActor(ctx context.Context, issueID, label string) (TriggerEvent, bool, error) {
	var data struct {
		Issue *struct {
			History struct {
				Nodes []struct {
					ID          string    `json:"id"`
					CreatedAt   time.Time `json:"createdAt"`
					Actor       *Person   `json:"actor"`
					AddedLabels []Label   `json:"addedLabels"`
				} `json:"nodes"`
			} `json:"history"`
		} `json:"issue"`
	}
	const q = `query SentinelaTriggerActor($id: String!) { issue(id: $id) { history(first: 100) { nodes { id createdAt actor { id name email } addedLabels { id name } } } } }`
	if err := c.query(ctx, q, map[string]any{"id": issueID}, &data); err != nil {
		return TriggerEvent{}, false, err
	}
	if data.Issue == nil {
		return TriggerEvent{}, false, fmt.Errorf("%w: issue %s", ErrNotFound, issueID)
	}
	var ev TriggerEvent
	found := false
	for _, h := range data.Issue.History.Nodes {
		for _, l := range h.AddedLabels {
			if strings.EqualFold(l.Name, label) && h.Actor != nil && (!found || h.CreatedAt.After(ev.At)) {
				ev, found = TriggerEvent{ID: h.ID, Actor: *h.Actor, At: h.CreatedAt}, true
			}
		}
	}
	return ev, found, nil
}
