package repo

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Checkout é um clone local de um repo, atualizado para a branch padrão.
type Checkout struct {
	Repo    string
	Dir     string
	Branch  string
	HeadSHA string
}

// Git sincroniza clones em Root/<owner>/<repo>. Token (opcional) vai por
// GIT_CONFIG_* no ambiente do processo git, nunca na URL nem em .git/config.
type Git struct {
	Root    string
	Token   string
	BaseURL string // padrão https://github.com
}

// Sync clona o repo na primeira vez e, depois, faz fetch e reset para origin/<branch>.
// O clone é só leitura para o Sentinela: o executor (Fase 4) usa worktrees separados.
func (g *Git) Sync(ctx context.Context, fullName, branch string) (*Checkout, error) {
	if strings.Count(fullName, "/") != 1 || strings.Contains(fullName, "..") {
		return nil, fmt.Errorf("nome de repo inválido %q", fullName)
	}
	base := g.BaseURL
	if base == "" {
		base = "https://github.com"
	}
	dir := filepath.Join(g.Root, filepath.FromSlash(fullName))
	if _, err := os.Stat(filepath.Join(dir, ".git")); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
			return nil, err
		}
		url := strings.TrimRight(base, "/") + "/" + fullName + ".git"
		if _, err := g.run(ctx, "", "clone", "--quiet", "--branch", branch, "--single-branch", url, dir); err != nil {
			return nil, err
		}
	} else {
		if _, err := g.run(ctx, dir, "fetch", "--quiet", "origin", branch); err != nil {
			return nil, err
		}
		if _, err := g.run(ctx, dir, "reset", "--quiet", "--hard", "origin/"+branch); err != nil {
			return nil, err
		}
	}
	head, err := g.run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	return &Checkout{Repo: fullName, Dir: dir, Branch: branch, HeadSHA: head}, nil
}

// ErrUnknownBase indica que o SHA base não existe no clone (ex.: force push); a
// rodada deve cair para análise completa.
var ErrUnknownBase = errors.New("SHA base não existe no clone")

// ChangedFiles lista os arquivos alterados entre base e head (adicionados, modificados, renomeados).
func (g *Git) ChangedFiles(ctx context.Context, c *Checkout, base string) ([]string, error) {
	if _, err := g.run(ctx, c.Dir, "cat-file", "-e", base+"^{commit}"); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnknownBase, base)
	}
	out, err := g.run(ctx, c.Dir, "diff", "--name-only", "--diff-filter=AMR", base, c.HeadSHA)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

func (g *Git) run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // binário fixo; argumentos montados pelo próprio pacote
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if g.Token != "" {
		auth := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + g.Token))
		cmd.Env = append(cmd.Env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Basic "+auth,
		)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// Permalink monta o link do GitHub para arquivo e linha num SHA fixo.
func Permalink(fullName, sha, file string, line int) string {
	return "https://github.com/" + fullName + "/blob/" + sha + "/" + file + "#L" + strconv.Itoa(line)
}
