// Package linear fala com a API GraphQL do Linear. Na Fase 1 só lê o workspace
// (times, estados, projetos e labels) para `config validate` e `config bootstrap`;
// publicação e sync de estado entram na Fase 3.
//
// Endpoint e autenticação: https://linear.app/developers/graphql (conferido em 2026-10-09).
// Chave pessoal vai crua no header Authorization, sem "Bearer"; o token do app OAuth vai como
// "Bearer <token>" (auth.go, ADR 0032).
package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const DefaultEndpoint = "https://api.linear.app/graphql"

// Team é um time do Linear com o que a config referencia.
type Team struct {
	ID       string
	Key      string
	Name     string
	States   []string
	Projects []string
	Labels   []string
}

// Workspace é a fotografia do workspace usada nas validações.
type Workspace struct {
	Teams []Team
}

// Team procura um time pela chave (ex.: EX).
func (w *Workspace) Team(key string) (*Team, bool) {
	for i := range w.Teams {
		if w.Teams[i].Key == key {
			return &w.Teams[i], true
		}
	}
	return nil, false
}

type Client struct {
	endpoint string
	auth     Auth
	http     *http.Client
	// legacyPrefixUntil: com o app, comentários com o prefixo "**Sentinela" anteriores a esta
	// data são do Sentinela da época da chave pessoal (ADR 0032).
	legacyPrefixUntil time.Time
}

// NewClient usa a chave pessoal (age como a pessoa dona dela).
func NewClient(endpoint, apiKey string) *Client {
	return NewClientAuth(endpoint, APIKey(apiKey), time.Time{})
}

// NewClientAuth usa a identidade dada: APIKey ou o app OAuth (AppAuth).
func NewClientAuth(endpoint string, auth Auth, legacyPrefixUntil time.Time) *Client {
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	return &Client{endpoint: endpoint, auth: auth, http: &http.Client{Timeout: 30 * time.Second}, legacyPrefixUntil: legacyPrefixUntil}
}

// AppActor diz se o cliente age como o app do Sentinela.
func (c *Client) AppActor() bool { return c.auth != nil && c.auth.AppActor() }

// O Linear limita a complexidade de cada consulta a 10.000 pontos; pedir times com
// estados, projetos e labels de uma vez passa disso (66.130 no workspace de teste, ADR 0008).
// Por isso a leitura é feita em duas etapas: times, e depois cada conexão por time,
// paginada por cursor.
const teamsQuery = `query SentinelaTeams($after: String) {
  teams(first: 50, after: $after) {
    nodes { id key name }
    pageInfo { hasNextPage endCursor }
  }
}`

// connectionQuery lê uma conexão de Team (states, projects ou labels) com paginação.
func connectionQuery(conn string) string {
	return `query SentinelaTeamConnection($id: String!, $after: String) {
  team(id: $id) {
    ` + conn + `(first: 100, after: $after) {
      nodes { name }
      pageInfo { hasNextPage endCursor }
    }
  }
}`
}

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

// Workspace lê times, estados, projetos e labels.
func (c *Client) Workspace(ctx context.Context) (*Workspace, error) {
	ws := &Workspace{}
	var after *string
	for {
		var data struct {
			Teams struct {
				Nodes []struct {
					ID   string `json:"id"`
					Key  string `json:"key"`
					Name string `json:"name"`
				} `json:"nodes"`
				PageInfo pageInfo `json:"pageInfo"`
			} `json:"teams"`
		}
		if err := c.query(ctx, teamsQuery, map[string]any{"after": after}, &data); err != nil {
			return nil, err
		}
		for _, t := range data.Teams.Nodes {
			ws.Teams = append(ws.Teams, Team{ID: t.ID, Key: t.Key, Name: t.Name})
		}
		if !data.Teams.PageInfo.HasNextPage {
			break
		}
		cursor := data.Teams.PageInfo.EndCursor
		after = &cursor
	}
	for i := range ws.Teams {
		t := &ws.Teams[i]
		var err error
		if t.States, err = c.teamNames(ctx, t.ID, "states"); err != nil {
			return nil, err
		}
		if t.Projects, err = c.teamNames(ctx, t.ID, "projects"); err != nil {
			return nil, err
		}
		if t.Labels, err = c.teamNames(ctx, t.ID, "labels"); err != nil {
			return nil, err
		}
	}
	return ws, nil
}

func (c *Client) teamNames(ctx context.Context, teamID, conn string) ([]string, error) {
	var out []string
	var after *string
	for {
		var data struct {
			Team map[string]struct {
				Nodes []struct {
					Name string `json:"name"`
				} `json:"nodes"`
				PageInfo pageInfo `json:"pageInfo"`
			} `json:"team"`
		}
		if err := c.query(ctx, connectionQuery(conn), map[string]any{"id": teamID, "after": after}, &data); err != nil {
			return nil, fmt.Errorf("lendo %s do time %s: %w", conn, teamID, err)
		}
		page := data.Team[conn]
		for _, n := range page.Nodes {
			out = append(out, n.Name)
		}
		if !page.PageInfo.HasNextPage {
			return out, nil
		}
		cursor := page.PageInfo.EndCursor
		after = &cursor
	}
}

func (c *Client) query(ctx context.Context, q string, vars map[string]any, out any) error {
	err := c.do(ctx, q, vars, out)
	if errors.Is(err, errUnauthorized) && c.AppActor() {
		// Token do app revogado ou expirado antes da hora: renova e tenta uma vez.
		c.auth.Invalidate()
		err = c.do(ctx, q, vars, out)
	}
	return err
}

var errUnauthorized = errors.New("linear: HTTP 401")

func (c *Client) do(ctx context.Context, q string, vars map[string]any, out any) error {
	if c.auth == nil {
		return errors.New("linear: sem credencial")
	}
	header, err := c.auth.Header(ctx)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"query": q, "variables": vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body)) //nolint:gosec // endpoint da API do Linear (LINEAR_API_URL só em testes)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", header)
	resp, err := c.http.Do(req) //nolint:gosec // idem
	if err != nil {
		return fmt.Errorf("linear: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return fmt.Errorf("linear: lendo resposta: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%w: %s", errUnauthorized, truncate(raw))
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("linear: HTTP %d: %s", resp.StatusCode, truncate(raw))
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("linear: resposta inválida: %w", err)
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("linear: %s", envelope.Errors[0].Message)
	}
	return json.Unmarshal(envelope.Data, out)
}

func truncate(b []byte) string {
	if len(b) > 300 {
		return string(b[:300]) + "..."
	}
	return string(b)
}
