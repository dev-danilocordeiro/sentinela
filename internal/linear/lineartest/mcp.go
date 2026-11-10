package lineartest

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/dev-danilocordeiro/sentinela/internal/mcpcall"
)

// FakeMCP é o Linear em memória atrás do MCP (adapters.tracker linear_mcp): faz o papel do `claude -p` com o MCP do Linear: lê os passos do prompt (ferramenta e
// argumentos em JSON) e responde sobre um Linear em memória.
type FakeMCP struct {
	Clock        func() time.Time
	LastListArgs map[string]any // argumentos da última listagem do ciclo
	Issues       []*FakeMCPIssue
	Calls        map[string]int              // ferramenta (e "op:<operação>") -> vezes
	Labels       []string                    // labels que existem no time
	Members      map[string][]map[string]any // time -> membros ({id, name, email}), para list_users
	mu           sync.Mutex
	seq          int
}

type FakeMCPIssue struct {
	UUID, Identifier, Title, Desc, State, StateType, Project string
	Labels                                                   []string
	Comments                                                 []map[string]any
	Updated                                                  time.Time
	Archived                                                 bool // fora da busca por marcador
}

var StateTypes = map[string]string{"Triage": "triage", "Backlog": "backlog", "Todo": "unstarted", "In Review": "started", "Done": "completed", "Canceled": "canceled"}

func (f *FakeMCP) now() time.Time {
	if f.Clock != nil {
		return f.Clock()
	}
	return time.Now()
}

func containsStr(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

var stepRe = regexp.MustCompile("Ferramenta `[^`]*__([a-z_]+)` com os argumentos: (\\{.*\\})")

func (f *FakeMCP) Run(_ context.Context, c mcpcall.Call, out any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c.Server != "claude.ai Linear" || c.Agent != "linear_mcp" {
		return fmt.Errorf("servidor %q agente %q", c.Server, c.Agent)
	}
	// Todo passo precisa caber no schema atual das ferramentas: o servidor recusa o resto, e o
	// modelo relata o desvio como erro (D-05 da revisão de prontidão).
	if err := CheckSteps(LinearMCPTools(), c.Steps); err != nil {
		return err
	}
	steps := stepRe.FindAllStringSubmatch(c.Prompt, -1)
	if len(steps) == 0 {
		return fmt.Errorf("prompt sem passos: %s", c.Prompt)
	}
	if f.Calls == nil {
		f.Calls = map[string]int{}
	}
	tool := steps[0][1]
	f.Calls[tool]++
	f.Calls["op:"+c.Op]++
	var args map[string]any
	if err := json.Unmarshal([]byte(steps[0][2]), &args); err != nil {
		return err
	}
	var resp any
	switch tool {
	case "list_issue_statuses":
		resp = map[string]any{"states": []string{"Triage", "Backlog", "Todo", "In Review", "Done", "Canceled"}, "labels": f.Labels}
	case "list_issues":
		if label, ok := args["label"].(string); ok {
			// Listagem do ciclo: issues com a label, atualizadas desde updatedAt (se houver).
			var since time.Time
			if u, ok := args["updatedAt"].(string); ok {
				since, _ = time.Parse(time.RFC3339, u)
			}
			f.LastListArgs = args
			var list []any
			for _, is := range f.Issues {
				if containsStr(is.Labels, label) && !is.Updated.Before(since) {
					list = append(list, is.view())
				}
			}
			resp = map[string]any{"issues": list}
			break
		}
		// Busca por marcador: as candidatas, como o modelo as devolveria depois do get_issue.
		q := args["query"].(string)
		if strings.Contains(q, " ") {
			return fmt.Errorf("a busca deveria ir só com o hash do marcador: %q", q)
		}
		list := []any{}
		for _, is := range f.Issues {
			if !strings.Contains(is.Desc, q) || is.Archived {
				continue
			}
			v := is.view()
			var lines []string
			for _, l := range strings.Split(is.Desc, "\n") {
				if strings.Contains(l, q) {
					lines = append(lines, l)
				}
			}
			v["markerLines"], v["createdAt"], v["archived"], v["project"] = lines, is.Updated.UTC().Format(time.RFC3339), false, is.Project
			list = append(list, v)
		}
		resp = map[string]any{"issues": list}
	case "save_issue":
		id, _ := args["id"].(string)
		if id == "" {
			f.seq++
			is := &FakeMCPIssue{UUID: fmt.Sprintf("uuid-%d", f.seq), Identifier: fmt.Sprintf("EX-%d", f.seq), Title: args["title"].(string),
				Desc: args["description"].(string), State: args["state"].(string), StateType: "backlog", Updated: time.Now()}
			is.Project, _ = args["project"].(string)
			for _, l := range args["labels"].([]any) {
				is.Labels = append(is.Labels, l.(string))
			}
			f.Issues = append(f.Issues, is)
			resp = map[string]any{"found": true, "issue": is.view()}
			break
		}
		is := f.Find(id)
		is.Labels = append(is.Labels, asStrings(args["addLabels"])...)
		if st, ok := args["state"].(string); ok {
			is.State, is.StateType = st, StateTypes[st]
		}
		is.Updated = f.now()
		resp = map[string]any{"ok": true}
	case "get_issue":
		if args["id"] == "<cada id>" {
			var list []any
			for _, is := range f.Issues {
				if strings.Contains(c.Prompt, is.UUID+",") || strings.Contains(c.Prompt, is.UUID+".") {
					list = append(list, is.view())
				}
			}
			resp = map[string]any{"issues": list}
			break
		}
		is := f.Find(args["id"].(string))
		resp = map[string]any{"issue": is.view(), "title": is.Title, "description": is.Desc, "comments": is.Comments, "stateActor": "Pessoa"}
	case "list_users":
		resp = map[string]any{"members": f.Members[fmt.Sprint(args["team"])]}
	case "save_comment":
		is := f.Find(args["issueId"].(string))
		is.Comments = append(is.Comments, map[string]any{"body": args["body"], "createdAt": time.Now().UTC().Format(time.RFC3339), "author": "Dona do MCP"})
		resp = map[string]any{"ok": true}
	default:
		return fmt.Errorf("ferramenta inesperada %s", tool)
	}
	b, _ := json.Marshal(resp)
	return json.Unmarshal(b, out)
}

func asStrings(v any) []string {
	var out []string
	if l, ok := v.([]any); ok {
		for _, x := range l {
			out = append(out, x.(string))
		}
	}
	return out
}

func (f *FakeMCP) Find(id string) *FakeMCPIssue {
	for _, is := range f.Issues {
		if is.UUID == id || is.Identifier == id {
			return is
		}
	}
	panic("issue " + id + " não existe")
}

func (is *FakeMCPIssue) view() map[string]any {
	return map[string]any{"uuid": is.UUID, "identifier": is.Identifier, "url": "https://linear.app/x/issue/" + is.Identifier,
		"updatedAt": is.Updated.UTC().Format(time.RFC3339), "statusName": is.State, "statusType": is.StateType, "labels": is.Labels}
}
