package github

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// GHCLI usa a conta do gh autenticado do usuário (adapters.git_host gh_cli, ADR 0038) no lugar
// das duas GitHub Apps: o token sai de `gh auth token` e vale para clone, push, PR em draft e a
// review do gate, que fica só como comentário (não dá para aprovar o próprio PR).
type GHCLI struct {
	Bin  string // padrão "gh"
	Base string // API REST; vazio usa DefaultBaseURL

	mu    sync.Mutex
	token string
}

func (g *GHCLI) bin() string {
	if g.Bin == "" {
		return "gh"
	}
	return g.Bin
}

// cmd roda o gh sem GITHUB_TOKEN no ambiente. O gh dá preferência a essa variável sobre a conta
// logada, e no Sentinela ela é outra coisa (o token de leitura dos clones, carregado do .env):
// com ela, o push saía com um token sem escrita (403 no teste do modo local). GH_TOKEN, que é do
// próprio gh, continua valendo.
func (g *GHCLI) cmd(ctx context.Context, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, g.bin(), args...) //nolint:gosec // binário da config
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GITHUB_TOKEN=") {
			c.Env = append(c.Env, kv)
		}
	}
	return c
}

// Token devolve o token do gh (lido uma vez por processo).
func (g *GHCLI) Token(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.token != "" {
		return g.token, nil
	}
	out, err := g.cmd(ctx, "auth", "token").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("gh auth token: %s (rode `gh auth login`)", strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("gh auth token: %w (instale o GitHub CLI e rode `gh auth login`)", err)
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return "", errors.New("gh auth token vazio: rode `gh auth login`")
	}
	g.token = tok
	return tok, nil
}

// APIBase é a URL da API REST.
func (g *GHCLI) APIBase() string {
	if g.Base == "" {
		return DefaultBaseURL
	}
	return g.Base
}

// Login devolve o usuário do gh (para o init e o validate).
func (g *GHCLI) Login(ctx context.Context) (string, error) {
	out, err := g.cmd(ctx, "api", "user", "--jq", ".login").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("gh api user: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("gh api user: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
