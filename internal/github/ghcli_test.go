package github

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeGH(t *testing.T, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o700); err != nil { //nolint:gosec // script de teste
		t.Fatal(err)
	}
	return p
}

func TestGHCLITokenComesFromGhAuthToken(t *testing.T) {
	calls := filepath.Join(t.TempDir(), "calls")
	g := &GHCLI{Bin: fakeGH(t, `echo "$@" >> `+calls+`
case "$1 $2" in
  "auth token") echo gho_teste ;;
  "api user") echo dev-exemplo ;;
esac`)}
	ctx := context.Background()
	for range 2 {
		tok, err := g.Token(ctx)
		if err != nil || tok != "gho_teste" {
			t.Fatalf("token = %q, %v", tok, err)
		}
	}
	if login, err := g.Login(ctx); err != nil || login != "dev-exemplo" {
		t.Errorf("login = %q, %v", login, err)
	}
	b, _ := os.ReadFile(calls)
	if strings.Count(string(b), "auth token") != 1 {
		t.Errorf("gh auth token chamado %d vezes (cache por processo):\n%s", strings.Count(string(b), "auth token"), b)
	}
	if g.APIBase() != DefaultBaseURL {
		t.Errorf("base = %s", g.APIBase())
	}
}

func TestGHCLINotLoggedInSaysWhatToDo(t *testing.T) {
	g := &GHCLI{Bin: fakeGH(t, `echo "You are not logged into any GitHub hosts." >&2; exit 1`)}
	if _, err := g.Token(context.Background()); err == nil || !strings.Contains(err.Error(), "gh auth login") || !strings.Contains(err.Error(), "not logged") {
		t.Errorf("erro = %v", err)
	}
	g = &GHCLI{Bin: filepath.Join(t.TempDir(), "nao-existe")}
	if _, err := g.Token(context.Background()); err == nil || !strings.Contains(err.Error(), "instale o GitHub CLI") {
		t.Errorf("sem gh: %v", err)
	}
}

// O GITHUB_TOKEN do Sentinela (leitura dos clones) não pode virar o token do gh: o gh prefere a
// variável à conta logada, e o push saía com um token sem escrita.
func TestGHCLIIgnoresTheSentinelaGitHubToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "token-de-leitura")
	t.Setenv("GH_TOKEN", "")
	g := &GHCLI{Bin: fakeGH(t, `if [ -n "$GITHUB_TOKEN" ]; then echo "$GITHUB_TOKEN"; else echo gho_conta_logada; fi`)}
	if tok, err := g.Token(context.Background()); err != nil || tok != "gho_conta_logada" {
		t.Errorf("token = %q, %v", tok, err)
	}
}
