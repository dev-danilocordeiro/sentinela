package linear

import "context"

// Tracker é o que o Sentinela usa do Linear: publicar issues, ler o estado e as decisões de volta
// e comentar ou mexer em labels e estados no fim de um job. Implementam: Client (API GraphQL, com o
// app OAuth ou a chave pessoal) e MCPTracker (o MCP do Linear do Claude Code do usuário, ADR 0039).
type Tracker interface {
	Workspace(ctx context.Context) (*Workspace, error)
	TeamRefs(ctx context.Context, key string) (*TeamRefs, error)
	ProjectID(ctx context.Context, teamID, name string) (string, error)
	LabelIDs(ctx context.Context, teamID string, names []string) ([]string, error)
	UserIDByEmail(ctx context.Context, email string) (string, error)
	// FindIssuesByMarker lista as candidatas da busca por marcador (marker.go escolhe a certa).
	FindIssuesByMarker(ctx context.Context, teamID, marker string) ([]MarkedIssue, error)
	CreateIssue(ctx context.Context, in IssueInput) (Issue, error)
	RelateIssues(ctx context.Context, issueID, relatedID string) error
	Issues(ctx context.Context, ids []string) ([]Issue, error)
	Closure(ctx context.Context, id string) (Closure, error)
	Detail(ctx context.Context, id string) (IssueDetail, error)
	Comment(ctx context.Context, issueID, body string) error
	AddLabel(ctx context.Context, issueID, labelID string) error
	DuplicateOf(ctx context.Context, id string) (*Issue, error)
	SetState(ctx context.Context, id, stateID string) error
	UpdateIssue(ctx context.Context, id, title, description string, addLabels, removeLabels []string) error
	// TeamMembers e TriggerActor servem a fix_trigger (ADR 0042): quem é do time dono e quem
	// aplicou a label de gatilho. O MCP do Linear não tem o histórico de labels: TriggerActor
	// devolve ErrUnsupported.
	TeamMembers(ctx context.Context, teamKey string) ([]Person, error)
	TriggerActor(ctx context.Context, issueID, label string) (TriggerEvent, bool, error)
}

var _ Tracker = (*Client)(nil)
