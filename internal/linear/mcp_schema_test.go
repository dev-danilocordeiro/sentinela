package linear_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/linear/lineartest"
	"github.com/dev-danilocordeiro/sentinela/internal/mcpcall"
)

// schemaRunner só confere os passos de cada chamada contra o schema gravado das ferramentas e
// devolve a resposta vazia; os métodos podem falhar depois por falta de dados, o que não importa.
type schemaRunner struct {
	tools map[string]lineartest.MCPTool
	errs  []error
	seen  map[string]bool
}

var errSchema = errors.New("schema")

func (r *schemaRunner) Run(_ context.Context, c mcpcall.Call, _ any) error {
	if len(c.Steps) == 0 {
		r.errs = append(r.errs, errors.New("chamada sem Steps: "+c.Op))
	}
	for _, s := range c.Steps {
		r.seen[s.Tool] = true
	}
	if err := lineartest.CheckSteps(r.tools, c.Steps); err != nil {
		r.errs = append(r.errs, err)
		return errSchema
	}
	return errors.New("resposta vazia")
}

// D-05 da revisão de prontidão: com limit 250 no list_projects (máximo 50 no MCP do Linear), o
// config validate de uma instalação MCP nova falhava. Toda chamada do MCPTracker cabe no schema
// atual das ferramentas e pagina pelo cursor.
func TestMCPCallsFitTheCurrentToolSchemas(t *testing.T) {
	r := &schemaRunner{tools: lineartest.LinearMCPTools(), seen: map[string]bool{}}
	m := &linear.MCPTracker{Run: r, Server: "claude.ai Linear", PollInterval: time.Minute, Teams: []string{"EX"}, FindingLabel: "sentinela",
		Now: func() time.Time { return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC) }}
	ctx := context.Background()
	_, _ = m.Workspace(ctx)
	_, _ = m.TeamRefs(ctx, "EX")
	_, _ = m.LabelIDs(ctx, "EX", []string{"sentinela"})
	_, _ = m.FindIssuesByMarker(ctx, "EX", "sentinela-fingerprint: abc")
	_, _ = m.CreateIssue(ctx, linear.IssueInput{TeamID: "EX", Title: "t", Description: "d", LabelIDs: []string{"sentinela"}})
	_ = m.RelateIssues(ctx, "EX-1", "EX-2")
	_, _ = m.Issues(ctx, []string{"EX-1"})
	_, _ = m.IssuesFresh(ctx, []string{"EX-1"})
	_, _ = m.Detail(ctx, "EX-1")
	_, _ = m.Closure(ctx, "EX-1")
	_ = m.Comment(ctx, "EX-1", "**Sentinela** oi")
	_ = m.AddLabel(ctx, "EX-1", "sentinela-fix")
	_ = m.SetState(ctx, "EX-1", "Done")
	_ = m.UpdateIssue(ctx, "EX-1", "t", "d", []string{"a"}, []string{"b"})
	_, _ = m.DuplicateOf(ctx, "EX-1")
	_, _ = m.TeamMembers(ctx, "EX")
	for _, err := range r.errs {
		t.Error(err)
	}
	for _, tool := range []string{"list_teams", "list_issue_labels", "list_projects", "list_issues", "list_users"} {
		if !r.seen[mcpcall.ToolPrefix("claude.ai Linear")+"__"+tool] {
			t.Errorf("%s não foi exercitado", tool)
		}
	}
}
