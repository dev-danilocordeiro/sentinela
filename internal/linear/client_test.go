package linear

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// O servidor devolve os projetos em duas páginas e recusa consultas que juntam
// times e conexões, como o limite de complexidade do Linear faz.
func TestWorkspacePaginatesConnectionsPerTeam(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "lin_x" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		conn := func(names []string, next string) map[string]any {
			nodes := []map[string]string{}
			for _, n := range names {
				nodes = append(nodes, map[string]string{"name": n})
			}
			return map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": next != "", "endCursor": next}}
		}
		var data map[string]any
		switch {
		case strings.Contains(req.Query, "teams(") && strings.Contains(req.Query, "states("):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errors":[{"message":"Query too complex"}]}`))
			return
		case strings.Contains(req.Query, "teams("):
			data = map[string]any{"teams": map[string]any{
				"nodes":    []any{map[string]any{"id": "t1", "key": "EX", "name": "time-exemplo"}},
				"pageInfo": map[string]any{"hasNextPage": false},
			}}
		case strings.Contains(req.Query, "projects("):
			if req.Variables["after"] == nil {
				data = map[string]any{"team": map[string]any{"projects": conn([]string{"app-java-angular"}, "c1")}}
			} else {
				data = map[string]any{"team": map[string]any{"projects": conn([]string{"razao-core"}, "")}}
			}
		case strings.Contains(req.Query, "states("):
			data = map[string]any{"team": map[string]any{"states": conn([]string{"Backlog"}, "")}}
		case strings.Contains(req.Query, "labels("):
			data = map[string]any{"team": map[string]any{"labels": conn([]string{"sentinela"}, "")}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()

	ws, err := NewClient(srv.URL, "lin_x").Workspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	team, ok := ws.Team("EX")
	if !ok {
		t.Fatal("time EX ausente")
	}
	if !slices.Equal(team.Projects, []string{"app-java-angular", "razao-core"}) {
		t.Errorf("projetos = %v, quer as duas páginas", team.Projects)
	}
	if !slices.Equal(team.States, []string{"Backlog"}) || !slices.Equal(team.Labels, []string{"sentinela"}) {
		t.Errorf("estados/labels = %v / %v", team.States, team.Labels)
	}
}

func TestWorkspaceSurfacesGraphQLErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"message":"Authentication required"}]}`))
	}))
	defer srv.Close()
	if _, err := NewClient(srv.URL, "x").Workspace(context.Background()); err == nil || !strings.Contains(err.Error(), "Authentication") {
		t.Fatalf("erro = %v", err)
	}
}
