package linear

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dev-danilocordeiro/sentinela/internal/mcpcall"
)

// MCPTracker fala com o Linear pelo MCP do Linear configurado no Claude Code do usuário
// (adapters.tracker linear_mcp, ADR 0039), sem chave nenhuma no Sentinela. Cada operação é uma
// chamada `claude -p` com só as ferramentas desse servidor permitidas e o modelo mais barato.
//
// Para gastar pouca cota:
//   - nomes no lugar de IDs: o save_issue do MCP aceita time, projeto, estado, labels e responsável
//     pelo nome, então TeamRefs, ProjectID, LabelIDs e UserIDByEmail devolvem os próprios nomes
//     (TeamRefs faz uma chamada por time e processo, para os estados e as labels existirem);
//   - Issues lista, a cada PollInterval, as issues com a label de achado atualizadas desde a
//     listagem anterior, uma chamada por ciclo para todos os times (veja Issues). O sync e o gatilho
//     do worker leem a cada ciclo, mas só vão ao Linear quando o intervalo vence. Antes de mudar o
//     estado de uma issue, o worker lê de novo (IssuesFresh), para não desfazer uma decisão humana.
//
// Identidade: as ações saem como a pessoa dona do MCP, não como um app. Os comentários do
// Sentinela levam o prefixo "**Sentinela" e são reconhecidos por ele (BySentinela).
type MCPTracker struct {
	Run          mcpcall.Runner
	Server       string        // ex.: "claude.ai Linear"
	PollInterval time.Duration // intervalo entre listagens (padrão 15m)
	Teams        []string      // chaves dos times do Linear da config: os que a listagem percorre
	FindingLabel string        // labels.finding (padrão sentinela)
	Now          func() time.Time

	mu       sync.Mutex
	teams    map[string]*mcpTeam
	issues   map[string]cachedIssue
	listedAt time.Time
	ws       *Workspace
	members  map[string]cachedMembers
}

type mcpTeam struct {
	refs   *TeamRefs
	labels map[string]bool
}

type cachedIssue struct {
	issue Issue
	at    time.Time
}

var _ Tracker = (*MCPTracker)(nil)

const mcpAgent = "linear_mcp"

func (m *MCPTracker) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *MCPTracker) tool(name string) string { return mcpcall.ToolPrefix(m.Server) + "__" + name }

func (m *MCPTracker) call(ctx context.Context, op, task string, schema map[string]any, out any, steps ...mcpcall.Step) error {
	props := schema["properties"].(map[string]any)
	props["error"] = map[string]any{"type": "string"}
	return m.Run.Run(ctx, mcpcall.Call{Agent: mcpAgent, Op: op, Server: m.Server, Prompt: mcpcall.Prompt(task, steps...), Schema: schema, Steps: steps}, out)
}

func obj(required []string, props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required}
}

var (
	strT    = map[string]any{"type": "string"}
	strArrT = map[string]any{"type": "array", "items": strT}
	// issueT é a forma em que cada issue volta do MCP.
	issueT = obj([]string{"uuid", "identifier", "url", "updatedAt", "statusName", "statusType", "labels"}, map[string]any{
		"uuid": strT, "identifier": strT, "url": strT, "updatedAt": strT,
		"statusName": strT, "statusType": strT, "labels": strArrT,
	})
	commentT = obj([]string{"body", "createdAt", "author"}, map[string]any{"body": strT, "createdAt": strT, "author": strT, "authorIsApp": map[string]any{"type": "boolean"}})
)

// pageLimit é o tamanho de página das listagens: o máximo que todas as ferramentas de listagem do
// MCP do Linear aceitam (list_projects para em 50; as outras, em 250). Com mais itens, o passo
// segue o cursor (pageNote). Um limit acima do máximo faz a ferramenta falhar, e o modelo relata o
// desvio como erro (D-05 da revisão de prontidão).
const pageLimit = 50

// pageNote manda seguir a paginação por cursor, para nenhuma listagem parar na primeira página.
const pageNote = "Se a resposta vier com hasNextPage true, chame de novo com os mesmos argumentos e o cursor dela, até acabar, e junte as páginas."

const issueNote = "Devolva uuid (o UUID da issue), identifier (ex.: EX-12), url, updatedAt (ISO 8601), statusName, statusType (triage, backlog, unstarted, started, completed, canceled ou duplicate) e os nomes das labels."

type mcpIssue struct {
	UUID       string   `json:"uuid"`
	Identifier string   `json:"identifier"`
	URL        string   `json:"url"`
	UpdatedAt  string   `json:"updatedAt"`
	StatusName string   `json:"statusName"`
	StatusType string   `json:"statusType"`
	Labels     []string `json:"labels"`
}

func (i mcpIssue) issue() Issue {
	out := Issue{ID: i.UUID, Identifier: i.Identifier, URL: i.URL}
	out.UpdatedAt, _ = time.Parse(time.RFC3339, i.UpdatedAt)
	out.State.Name, out.State.Type = i.StatusName, strings.ToLower(i.StatusType)
	for _, l := range i.Labels {
		out.Labels.Nodes = append(out.Labels.Nodes, Label{ID: l, Name: l})
	}
	return out
}

type mcpComment struct {
	Body        string `json:"body"`
	CreatedAt   string `json:"createdAt"`
	Author      string `json:"author"`
	AuthorIsApp bool   `json:"authorIsApp"`
}

// comments converte os comentários; os do Sentinela são os que começam com o prefixo.
func comments(in []mcpComment) []IssueComment {
	out := make([]IssueComment, 0, len(in))
	for _, c := range in {
		at, _ := time.Parse(time.RFC3339, c.CreatedAt)
		cm := IssueComment{Body: c.Body, CreatedAt: at, User: &Actor{Name: c.Author, App: c.AuthorIsApp}}
		cm.BySentinela = IsSentinelaComment(c.Body)
		out = append(out, cm)
	}
	return out
}

func errOf(e string) error {
	if strings.TrimSpace(e) == "" {
		return nil
	}
	return fmt.Errorf("%w: %s", mcpcall.ErrMCP, e)
}

// Workspace lista times, estados, labels e projetos (uma chamada; só o config validate usa).
func (m *MCPTracker) Workspace(ctx context.Context) (*Workspace, error) {
	m.mu.Lock()
	if m.ws != nil {
		defer m.mu.Unlock()
		return m.ws, nil
	}
	m.mu.Unlock()
	var out struct {
		Teams []struct {
			ID       string   `json:"id"`
			Key      string   `json:"key"`
			Name     string   `json:"name"`
			States   []string `json:"states"`
			Labels   []string `json:"labels"`
			Projects []string `json:"projects"`
		} `json:"teams"`
		Error string `json:"error"`
	}
	schema := obj([]string{"teams"}, map[string]any{"teams": map[string]any{"type": "array", "items": obj([]string{"id", "key", "name", "states", "labels", "projects"}, map[string]any{
		"id": strT, "key": strT, "name": strT, "states": strArrT, "labels": strArrT, "projects": strArrT,
	})}})
	err := m.call(ctx, "workspace", "listar os times do Linear com os nomes dos estados, das labels (do time e do workspace) e dos projetos de cada um", schema, &out,
		mcpcall.Step{Tool: m.tool("list_teams"), Args: map[string]any{"limit": pageLimit}, Note: pageNote},
		mcpcall.Step{Tool: m.tool("list_issue_statuses"), Args: map[string]any{"team": "<cada time>"}, Note: "Uma vez para cada time."},
		mcpcall.Step{Tool: m.tool("list_issue_labels"), Args: map[string]any{"team": "<cada time>", "limit": pageLimit}, Note: "Uma vez para cada time; inclua as labels do workspace que a ferramenta devolver. " + pageNote},
		mcpcall.Step{Tool: m.tool("list_projects"), Args: map[string]any{"team": "<cada time>", "limit": pageLimit}, Note: "Uma vez para cada time. " + pageNote},
	)
	if err == nil {
		err = errOf(out.Error)
	}
	if err != nil {
		return nil, err
	}
	ws := &Workspace{}
	for _, t := range out.Teams {
		ws.Teams = append(ws.Teams, Team{ID: t.ID, Key: t.Key, Name: t.Name, States: t.States, Labels: t.Labels, Projects: t.Projects})
	}
	m.mu.Lock()
	m.ws = ws
	m.mu.Unlock()
	return ws, nil
}

// TeamRefs lê os estados e as labels do time (uma chamada por time e processo). O ID é a própria
// chave: o MCP aceita o time pelo nome ou pela chave.
func (m *MCPTracker) TeamRefs(ctx context.Context, key string) (*TeamRefs, error) {
	m.mu.Lock()
	if t, ok := m.teams[key]; ok {
		m.mu.Unlock()
		return t.refs, nil
	}
	m.mu.Unlock()
	var out struct {
		States []string `json:"states"`
		Labels []string `json:"labels"`
		Error  string   `json:"error"`
	}
	err := m.call(ctx, "teams", "ler os estados e as labels do time "+key+" do Linear", obj([]string{"states", "labels"}, map[string]any{"states": strArrT, "labels": strArrT}), &out,
		mcpcall.Step{Tool: m.tool("list_issue_statuses"), Args: map[string]any{"team": key}, Note: "Devolva em states o nome de cada estado."},
		mcpcall.Step{Tool: m.tool("list_issue_labels"), Args: map[string]any{"team": key, "limit": pageLimit}, Note: "Devolva em labels o nome de cada label (as do time e as do workspace). " + pageNote},
	)
	if err == nil {
		err = errOf(out.Error)
	}
	if err != nil {
		return nil, err
	}
	if len(out.States) == 0 {
		return nil, fmt.Errorf("%w: time %q", ErrNotFound, key)
	}
	t := &mcpTeam{refs: &TeamRefs{ID: key, Key: key, States: map[string]string{}}, labels: map[string]bool{}}
	for _, s := range out.States {
		t.refs.States[s] = s
	}
	for _, l := range out.Labels {
		t.labels[strings.ToLower(l)] = true
	}
	m.mu.Lock()
	if m.teams == nil {
		m.teams = map[string]*mcpTeam{}
	}
	m.teams[key] = t
	m.mu.Unlock()
	return t.refs, nil
}

// ProjectID devolve o nome: o save_issue aceita o projeto pelo nome.
func (m *MCPTracker) ProjectID(_ context.Context, _, name string) (string, error) { return name, nil }

// LabelIDs confere as labels nas do time (lidas no TeamRefs) e devolve os nomes.
func (m *MCPTracker) LabelIDs(ctx context.Context, teamID string, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	if _, err := m.TeamRefs(ctx, teamID); err != nil {
		return nil, err
	}
	m.mu.Lock()
	t := m.teams[teamID]
	m.mu.Unlock()
	for _, n := range names {
		if !t.labels[strings.ToLower(n)] {
			return nil, fmt.Errorf("%w: label %q", ErrNotFound, n)
		}
	}
	return append([]string(nil), names...), nil
}

// UserIDByEmail devolve o e-mail: o save_issue aceita o responsável pelo e-mail.
func (m *MCPTracker) UserIDByEmail(_ context.Context, email string) (string, error) {
	return email, nil
}

func (m *MCPTracker) oneIssue(ctx context.Context, op, task string, steps ...mcpcall.Step) (*Issue, error) {
	var out struct {
		Found bool     `json:"found"`
		Issue mcpIssue `json:"issue"`
		Error string   `json:"error"`
	}
	if err := m.call(ctx, op, task, obj([]string{"found"}, map[string]any{"found": map[string]any{"type": "boolean"}, "issue": issueT}), &out, steps...); err != nil {
		return nil, err
	}
	if err := errOf(out.Error); err != nil {
		return nil, err
	}
	if !out.Found || out.Issue.UUID == "" {
		return nil, nil
	}
	is := out.Issue.issue()
	m.remember(is)
	return &is, nil
}

// FindIssuesByMarker lista as issues do time que podem ter o marcador na descrição (publicação
// idempotente). A busca do MCP é aproximada e trunca as descrições: busca pelo hash do marcador e
// confere cada candidato com get_issue. Devolve, de cada um, as linhas da descrição com o hash, a
// criação, se está arquivada e o nome do projeto (o ProjectID do MCP é o nome); pickMarked escolhe.
func (m *MCPTracker) FindIssuesByMarker(ctx context.Context, teamID, marker string) ([]MarkedIssue, error) {
	query := marker
	if f := strings.Fields(marker); len(f) > 0 {
		query = f[len(f)-1]
	}
	candT := obj([]string{"uuid", "identifier", "url", "updatedAt", "statusName", "statusType", "labels", "markerLines", "createdAt", "archived", "project"}, map[string]any{
		"uuid": strT, "identifier": strT, "url": strT, "updatedAt": strT, "statusName": strT, "statusType": strT, "labels": strArrT,
		"markerLines": strArrT, "createdAt": strT, "archived": map[string]any{"type": "boolean"}, "project": strT,
	})
	var out struct {
		Issues []struct {
			mcpIssue
			MarkerLines []string `json:"markerLines"`
			CreatedAt   string   `json:"createdAt"`
			Archived    bool     `json:"archived"`
			Project     string   `json:"project"`
		} `json:"issues"`
		Error string `json:"error"`
	}
	err := m.call(ctx, "find", "listar as issues do time "+teamID+" cuja descrição contém o texto `"+marker+"`. Nenhuma é um resultado normal: issues vazio, sem error",
		obj([]string{"issues"}, map[string]any{"issues": map[string]any{"type": "array", "items": candT}}), &out,
		mcpcall.Step{Tool: m.tool("list_issues"), Args: map[string]any{"team": teamID, "query": query, "includeArchived": false, "limit": pageLimit,
			"fields": []string{"id", "uuid", "url", "updatedAt", "status", "statusType", "labels"}},
			Note: "Estes são só candidatos: a busca é aproximada. " + pageNote},
		mcpcall.Step{Tool: m.tool("get_issue"), Args: map[string]any{"id": "<cada candidato>", "fields": []string{"id", "uuid", "url", "createdAt", "updatedAt", "archivedAt", "status", "statusType", "labels", "project", "description"}},
			Note: "Devolva só os candidatos cuja descrição completa contém o texto exato `" + marker + "`. Para cada um: markerLines (as linhas da descrição que contêm " + query + ", sem mudar nada), createdAt (ISO 8601), archived (archivedAt preenchido) e project (o nome do projeto, vazio se não tiver). " + issueNote},
	)
	if err != nil {
		return nil, err
	}
	if err := errOf(out.Error); err != nil {
		return nil, err
	}
	res := make([]MarkedIssue, 0, len(out.Issues))
	for _, c := range out.Issues {
		if c.UUID == "" {
			continue
		}
		is := MarkedIssue{Issue: c.issue(), Description: strings.Join(c.MarkerLines, "\n"), ProjectID: c.Project, Archived: c.Archived}
		is.CreatedAt, _ = time.Parse(time.RFC3339, c.CreatedAt)
		m.remember(is.Issue)
		res = append(res, is)
	}
	return res, nil
}

// CreateIssue cria a issue (time, projeto, estado, labels e responsável pelos nomes).
func (m *MCPTracker) CreateIssue(ctx context.Context, in IssueInput) (Issue, error) {
	args := map[string]any{"team": in.TeamID, "title": in.Title, "description": in.Description}
	if in.ProjectID != "" {
		args["project"] = in.ProjectID
	}
	if in.StateID != "" {
		args["state"] = in.StateID
	}
	if len(in.LabelIDs) > 0 {
		args["labels"] = in.LabelIDs
	}
	if in.AssigneeID != "" {
		args["assignee"] = in.AssigneeID
	}
	if in.Priority > 0 {
		args["priority"] = in.Priority
	}
	is, err := m.oneIssue(ctx, "create", "criar uma issue no Linear",
		mcpcall.Step{Tool: m.tool("save_issue"), Args: args, Note: "Passe a description exatamente como está, com as quebras de linha. found=true com a issue criada. " + issueNote})
	if err != nil {
		return Issue{}, err
	}
	if is == nil {
		return Issue{}, fmt.Errorf("%w: save_issue não devolveu a issue criada", mcpcall.ErrMCP)
	}
	return *is, nil
}

func (m *MCPTracker) save(ctx context.Context, task string, args map[string]any) error {
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := m.call(ctx, "save", task, obj([]string{"ok"}, map[string]any{"ok": map[string]any{"type": "boolean"}}), &out,
		mcpcall.Step{Tool: m.tool("save_issue"), Args: args, Note: "ok=true se a ferramenta salvou."}); err != nil {
		return err
	}
	if err := errOf(out.Error); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("%w: save_issue não confirmou (%s)", mcpcall.ErrMCP, task)
	}
	return nil
}

func (m *MCPTracker) RelateIssues(ctx context.Context, issueID, relatedID string) error {
	return m.save(ctx, "relacionar duas issues", map[string]any{"id": issueID, "relatedTo": []string{relatedID}})
}

func (m *MCPTracker) remember(is Issue) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.issues == nil {
		m.issues = map[string]cachedIssue{}
	}
	m.issues[is.ID] = cachedIssue{issue: is, at: m.now()}
}

func (m *MCPTracker) interval() time.Duration {
	if m.PollInterval <= 0 {
		return 15 * time.Minute
	}
	return m.PollInterval
}

// Issues devolve o estado e as labels das issues. A cada PollInterval, uma chamada lista, por
// time, as issues com a label de achado atualizadas desde a listagem anterior (na primeira, todas):
// o custo de um ciclo é uma chamada `claude -p`, qualquer que seja o número de issues abertas, e o
// que volta são só as que mudaram. Entre listagens, vale o estado guardado. Issues que nenhuma
// listagem trouxe (sem a label, ou de outro time) são lidas com get_issue numa chamada só.
func (m *MCPTracker) Issues(ctx context.Context, ids []string) ([]Issue, error) {
	if err := m.refresh(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	var missing []string
	for _, id := range ids {
		if _, ok := m.issues[id]; !ok {
			missing = append(missing, id)
		}
	}
	m.mu.Unlock()
	if len(missing) > 0 {
		if err := m.get(ctx, "get", missing); err != nil {
			return nil, err
		}
	}
	return m.cached(ids), nil
}

// IssuesFresh lê as issues agora, sem o estado guardado: antes de uma escrita que depende do
// estado atual (mover para revisão ou concluído), para não desfazer uma decisão humana recente.
func (m *MCPTracker) IssuesFresh(ctx context.Context, ids []string) ([]Issue, error) {
	if err := m.get(ctx, "get_fresh", ids); err != nil {
		return nil, err
	}
	return m.cached(ids), nil
}

func (m *MCPTracker) cached(ids []string) []Issue {
	m.mu.Lock()
	defer m.mu.Unlock()
	var res []Issue
	for _, id := range ids {
		if c, ok := m.issues[id]; ok {
			res = append(res, c.issue)
		}
	}
	return res
}

var issueListT = obj([]string{"issues"}, map[string]any{"issues": map[string]any{"type": "array", "items": issueT}})

// refresh faz a listagem por time quando o intervalo venceu.
func (m *MCPTracker) refresh(ctx context.Context) error {
	m.mu.Lock()
	last := m.listedAt
	due := last.IsZero() || m.now().Sub(last) >= m.interval()
	m.mu.Unlock()
	if !due || len(m.Teams) == 0 {
		return nil
	}
	started := m.now()
	args := map[string]any{"team": "<cada time>", "label": m.label(), "includeArchived": true, "limit": pageLimit, "orderBy": "updatedAt",
		"fields": []string{"id", "uuid", "url", "updatedAt", "status", "statusType", "labels"}}
	task := "listar as issues do Sentinela (label " + m.label() + ") nos times " + strings.Join(m.Teams, ", ")
	if !last.IsZero() {
		// Margem de 2 minutos contra diferença de relógio.
		args["updatedAt"] = last.Add(-2 * time.Minute).UTC().Format(time.RFC3339)
		task += " atualizadas desde a listagem anterior"
	}
	var out struct {
		Issues []mcpIssue `json:"issues"`
		Error  string     `json:"error"`
	}
	err := m.call(ctx, "poll", task, issueListT, &out,
		mcpcall.Step{Tool: m.tool("list_issues"), Args: args,
			Note: "Uma vez para cada time: " + strings.Join(m.Teams, ", ") + ". " + pageNote + " " + issueNote})
	if err == nil {
		err = errOf(out.Error)
	}
	if err != nil {
		return err
	}
	for _, i := range out.Issues {
		m.remember(i.issue())
	}
	m.mu.Lock()
	m.listedAt = started
	m.mu.Unlock()
	return nil
}

func (m *MCPTracker) label() string {
	if m.FindingLabel == "" {
		return "sentinela"
	}
	return m.FindingLabel
}

// get lê as issues pelo id, numa chamada.
func (m *MCPTracker) get(ctx context.Context, op string, ids []string) error {
	var out struct {
		Issues []mcpIssue `json:"issues"`
		Error  string     `json:"error"`
	}
	err := m.call(ctx, op, "ler o estado e as labels destas issues do Linear: "+strings.Join(ids, ", "), issueListT, &out,
		mcpcall.Step{Tool: m.tool("get_issue"), Args: map[string]any{"id": "<cada id>", "fields": []string{"id", "uuid", "url", "updatedAt", "status", "statusType", "labels"}},
			Note: "Uma vez para cada id da lista: " + strings.Join(ids, ", ") + ". Pule as que não existem mais. " + issueNote})
	if err == nil {
		err = errOf(out.Error)
	}
	if err != nil {
		return err
	}
	for _, i := range out.Issues {
		m.remember(i.issue())
	}
	return nil
}

func (m *MCPTracker) detail(ctx context.Context, id string, history bool) (IssueDetail, string, error) {
	var out struct {
		Issue       mcpIssue     `json:"issue"`
		Title       string       `json:"title"`
		Description string       `json:"description"`
		Comments    []mcpComment `json:"comments"`
		StateActor  string       `json:"stateActor"`
		Error       string       `json:"error"`
	}
	fields := []string{"id", "uuid", "url", "updatedAt", "status", "statusType", "labels", "title", "description"}
	note := "Devolva issue (" + strings.TrimPrefix(issueNote, "Devolva ") + "), title e description exatamente como estão."
	if history {
		fields = append(fields, "stateHistory")
		note += " Em stateActor, o nome de quem fez a última mudança de estado (vazio se o histórico não disser)."
	}
	op := "detail"
	if history {
		op = "closure"
	}
	err := m.call(ctx, op, "ler a issue "+id+" do Linear com os comentários",
		obj([]string{"issue", "title", "description", "comments"}, map[string]any{"issue": issueT, "title": strT, "description": strT,
			"comments": map[string]any{"type": "array", "items": commentT}, "stateActor": strT}), &out,
		mcpcall.Step{Tool: m.tool("get_issue"), Args: map[string]any{"id": id, "fields": fields}, Note: note},
		mcpcall.Step{Tool: m.tool("list_comments"), Args: map[string]any{"issueId": id, "limit": pageLimit, "orderBy": "createdAt"},
			Note: "Devolva cada comentário com body (exato), createdAt (ISO 8601), author (nome) e authorIsApp. " + pageNote})
	if err == nil {
		err = errOf(out.Error)
	}
	if err != nil {
		return IssueDetail{}, "", err
	}
	d := IssueDetail{Issue: out.Issue.issue(), Title: out.Title, Description: out.Description}
	d.Comments.Nodes = comments(out.Comments)
	m.remember(d.Issue)
	return d, out.StateActor, nil
}

func (m *MCPTracker) Detail(ctx context.Context, id string) (IssueDetail, error) {
	d, _, err := m.detail(ctx, id, false)
	return d, err
}

// Closure: o motivo é o último comentário de uma pessoa que não é do Sentinela.
func (m *MCPTracker) Closure(ctx context.Context, id string) (Closure, error) {
	d, actor, err := m.detail(ctx, id, true)
	if err != nil {
		return Closure{}, err
	}
	cl := Closure{Issue: d.Issue, Actor: actor}
	for i := len(d.Comments.Nodes) - 1; i >= 0; i-- {
		if cm := d.Comments.Nodes[i]; cm.Human() {
			cl.Reason = strings.TrimSpace(cm.Body)
			break
		}
	}
	return cl, nil
}

// Comment comenta como a pessoa dona do MCP; o prefixo identifica o Sentinela.
func (m *MCPTracker) Comment(ctx context.Context, issueID, body string) error {
	if !IsSentinelaComment(body) {
		body = "**Sentinela:** " + body
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := m.call(ctx, "comment", "comentar na issue "+issueID, obj([]string{"ok"}, map[string]any{"ok": map[string]any{"type": "boolean"}}), &out,
		mcpcall.Step{Tool: m.tool("save_comment"), Args: map[string]any{"issueId": issueID, "body": body}, Note: "Passe o body exatamente como está. ok=true se comentou."}); err != nil {
		return err
	}
	if err := errOf(out.Error); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("%w: save_comment não confirmou", mcpcall.ErrMCP)
	}
	return nil
}

func (m *MCPTracker) AddLabel(ctx context.Context, issueID, labelID string) error {
	return m.save(ctx, "aplicar a label "+labelID, map[string]any{"id": issueID, "addLabels": []string{labelID}})
}

func (m *MCPTracker) SetState(ctx context.Context, id, stateID string) error {
	return m.save(ctx, "mover a issue para "+stateID, map[string]any{"id": id, "state": stateID})
}

func (m *MCPTracker) UpdateIssue(ctx context.Context, id, title, description string, addLabels, removeLabels []string) error {
	args := map[string]any{"id": id}
	if title != "" {
		args["title"] = title
	}
	if description != "" {
		args["description"] = description
	}
	if len(addLabels) > 0 {
		args["addLabels"] = addLabels
	}
	if len(removeLabels) > 0 {
		args["removeLabels"] = removeLabels
	}
	return m.save(ctx, "atualizar a issue", args)
}

// DuplicateOf lê a issue original de uma marcada como duplicata.
func (m *MCPTracker) DuplicateOf(ctx context.Context, id string) (*Issue, error) {
	is, err := m.oneIssue(ctx, "duplicate", "achar a issue original da duplicata "+id,
		mcpcall.Step{Tool: m.tool("get_issue"), Args: map[string]any{"id": id, "includeRelations": true},
			Note: "Na relação de duplicata (duplicateOf), pegue o identificador da issue original."},
		mcpcall.Step{Tool: m.tool("get_issue"), Args: map[string]any{"id": "<identificador da original>", "fields": []string{"id", "uuid", "url", "updatedAt", "status", "statusType", "labels"}},
			Note: "Só se houver original. found=false se não houver. " + issueNote})
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return is, err
}

// TeamMembers lê os membros do time numa chamada (list_users com o time) e guarda por
// PollInterval: o gatilho e as instruções da equipe consultam a cada ciclo (ADR 0042).
func (m *MCPTracker) TeamMembers(ctx context.Context, teamKey string) ([]Person, error) {
	m.mu.Lock()
	if c, ok := m.members[teamKey]; ok && m.now().Sub(c.at) < m.interval() {
		m.mu.Unlock()
		return c.people, nil
	}
	m.mu.Unlock()
	var out struct {
		Members []Person `json:"members"`
		Error   string   `json:"error"`
	}
	personT := obj([]string{"id", "name", "email"}, map[string]any{"id": strT, "name": strT, "email": strT})
	err := m.call(ctx, "members", "listar os membros do time "+teamKey+" do Linear",
		obj([]string{"members"}, map[string]any{"members": map[string]any{"type": "array", "items": personT}}), &out,
		mcpcall.Step{Tool: m.tool("list_users"), Args: map[string]any{"team": teamKey, "limit": pageLimit},
			Note: "Devolva em members o id, o nome e o e-mail de cada usuário. " + pageNote})
	if err == nil {
		err = errOf(out.Error)
	}
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.members == nil {
		m.members = map[string]cachedMembers{}
	}
	m.members[teamKey] = cachedMembers{people: out.Members, at: m.now()}
	m.mu.Unlock()
	return out.Members, nil
}

type cachedMembers struct {
	people []Person
	at     time.Time
}

// TriggerActor não existe no MCP: as ferramentas do MCP do Linear não dão o histórico de labels
// de uma issue. No modo MCP, fix_trigger filtra só as instruções da equipe (ADR 0042).
func (m *MCPTracker) TriggerActor(context.Context, string, string) (TriggerEvent, bool, error) {
	return TriggerEvent{}, false, ErrUnsupported
}
