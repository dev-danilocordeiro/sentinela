package repo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BranchPrefix é o prefixo obrigatório das branches do Sentinela; push fora dele é recusado.
const BranchPrefix = "sentinela/"

// Run roda um comando git em dir com a autenticação do Git (token por header, nunca na URL).
func (g *Git) Run(ctx context.Context, dir string, args ...string) (string, error) {
	return g.run(ctx, dir, args...)
}

// Worktree garante um git worktree em path, na branch dada, criado a partir de from (ex.:
// origin/main). Se o worktree já existe (rework do mesmo job), só confere a branch.
func (g *Git) Worktree(ctx context.Context, c *Checkout, path, branch, from string) error {
	if !strings.HasPrefix(branch, BranchPrefix) {
		return fmt.Errorf("branch %q fora do prefixo %s", branch, BranchPrefix)
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		cur, err := g.run(ctx, path, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return err
		}
		if cur != branch {
			return fmt.Errorf("worktree %s está na branch %s, esperava %s", path, cur, branch)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	_, _ = g.run(ctx, c.Dir, "worktree", "prune")
	_, err := g.run(ctx, c.Dir, "worktree", "add", "-B", branch, path, from)
	return err
}

// FetchBranch traz origin/<branch> para o clone (o clone é single-branch).
func (g *Git) FetchBranch(ctx context.Context, c *Checkout, branch string) error {
	_, err := g.run(ctx, c.Dir, "fetch", "--quiet", "origin", "+refs/heads/"+branch+":refs/remotes/origin/"+branch)
	return err
}

// Push envia a branch do worktree para origin. Só aceita branches sentinela/* e nunca a padrão:
// o Sentinela não empurra para a branch padrão de nenhum repo.
func (g *Git) Push(ctx context.Context, dir, branch, defaultBranch string) error {
	if !strings.HasPrefix(branch, BranchPrefix) || branch == defaultBranch {
		return fmt.Errorf("push recusado para %q: só branches %s*", branch, BranchPrefix)
	}
	_, err := g.run(ctx, dir, "push", "--quiet", "origin", "HEAD:refs/heads/"+branch)
	return err
}
