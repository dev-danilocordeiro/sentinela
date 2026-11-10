// Package github fala com a API REST do GitHub. Na Fase 1 só lê: existência de
// repos, times e usuários, e o conteúdo do CODEOWNERS. As GitHub Apps
// (a bot e a revisora, github_apps no sentinela.yaml) ficam em app.go.
//
// Endpoints (conferidos em 2026-10-09, API version 2022-11-28):
//   - GET /repos/{owner}/{repo}                 https://docs.github.com/en/rest/repos/repos#get-a-repository
//   - GET /repos/{owner}/{repo}/contents/{path} https://docs.github.com/en/rest/repos/contents#get-repository-content
//   - GET /orgs/{org}/teams/{team_slug}         https://docs.github.com/en/rest/teams/teams#get-a-team-by-name
//   - GET /users/{username}                     https://docs.github.com/en/rest/users/users#get-a-user
//   - GET /repos/{owner}/{repo}/languages       https://docs.github.com/en/rest/repos/repos#list-repository-languages
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dev-danilocordeiro/sentinela/internal/repo"
)

const DefaultBaseURL = "https://api.github.com"

// ErrNotFound indica 404 na API.
var ErrNotFound = errors.New("não encontrado no GitHub")

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient cria o cliente. token pode ser vazio (limites de API anônima) e vem de GITHUB_TOKEN.
func NewClient(baseURL, token string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: &http.Client{Timeout: 30 * time.Second}}
}

// RepoExists confere se owner/repo existe e é visível com o token.
func (c *Client) RepoExists(ctx context.Context, fullName string) (bool, error) {
	return c.exists(ctx, "/repos/"+fullName)
}

// TeamExists confere @org/slug.
func (c *Client) TeamExists(ctx context.Context, org, slug string) (bool, error) {
	return c.exists(ctx, "/orgs/"+url.PathEscape(org)+"/teams/"+url.PathEscape(slug))
}

// UserExists confere um login.
func (c *Client) UserExists(ctx context.Context, login string) (bool, error) {
	return c.exists(ctx, "/users/"+url.PathEscape(login))
}

// Codeowners lê o CODEOWNERS do repo na ordem de precedência do GitHub; nil se não houver.
func (c *Client) Codeowners(ctx context.Context, fullName string) (*repo.Codeowners, error) {
	for _, p := range repo.CodeownersPaths {
		body, err := c.get(ctx, "/repos/"+fullName+"/contents/"+p, "application/vnd.github.raw+json")
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return repo.ParseCodeowners(strings.NewReader(string(body)))
	}
	return nil, nil
}

// CommandsDoc devolve o primeiro arquivo de comandos (repo.CommandsDocs: AGENTS.md, CLAUDE.md)
// que existe na raiz do repo, na branch ref (vazio: a padrão do repo); "" se nenhum existe.
func (c *Client) CommandsDoc(ctx context.Context, fullName, ref string) (string, error) {
	for _, name := range repo.CommandsDocs {
		path := "/repos/" + fullName + "/contents/" + name
		if ref != "" {
			path += "?ref=" + url.QueryEscape(ref)
		}
		ok, err := c.exists(ctx, path)
		if err != nil {
			return "", err
		}
		if ok {
			return name, nil
		}
	}
	return "", nil
}

// Languages devolve os bytes de código por linguagem do repo, como o linguist do GitHub conta
// (https://docs.github.com/en/rest/repos/repos#list-repository-languages).
func (c *Client) Languages(ctx context.Context, fullName string) (map[string]int64, error) {
	body, err := c.get(ctx, "/repos/"+fullName+"/languages", "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	var out map[string]int64
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("github: linguagens de %s: %w", fullName, err)
	}
	return out, nil
}

// CodeownersSource adapta o cliente para o resolvedor, com cache por repo.
type CodeownersSource struct {
	ctx    context.Context //nolint:containedctx // adaptador para a interface síncrona do resolvedor
	client *Client
	cache  map[string]*repo.Codeowners
}

func (c *Client) CodeownersSource(ctx context.Context) *CodeownersSource {
	return &CodeownersSource{ctx: ctx, client: c, cache: map[string]*repo.Codeowners{}}
}

func (s *CodeownersSource) Codeowners(name string) (*repo.Codeowners, error) {
	if co, ok := s.cache[name]; ok {
		return co, nil
	}
	co, err := s.client.Codeowners(s.ctx, name)
	if err != nil {
		return nil, err
	}
	s.cache[name] = co
	return co, nil
}

func (c *Client) exists(ctx context.Context, path string) (bool, error) {
	_, err := c.get(ctx, path, "application/vnd.github+json")
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (c *Client) get(ctx context.Context, path, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return nil, fmt.Errorf("github: lendo resposta: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("github: GET %s: HTTP %d", path, resp.StatusCode)
	}
	return body, nil
}
