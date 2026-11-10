package discovery

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
)

// Publicação parada no limite de uso da assinatura (ADR 0055): a rodada guarda em
// stats.issues.paused_until quando a cota volta, e o serve retoma depois dele com ResumePublish,
// sem analisar o repo de novo.

// PausedPublication é um repo cuja última rodada de análise parou a publicação no limite de uso.
type PausedPublication struct {
	Repo  string
	RunID int64
	Until time.Time
}

// PausedPublications lista os repos com a publicação pausada pelo limite de uso.
func (r *Runner) PausedPublications(ctx context.Context) ([]PausedPublication, error) {
	rows, err := r.Store.Q.PausedPublications(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PausedPublication, 0, len(rows))
	for _, row := range rows {
		out = append(out, PausedPublication{Repo: row.Repo, RunID: row.ID, Until: row.PausedUntil.Time})
	}
	return out, nil
}

// ResumePublish publica os pendentes do repo numa rodada sem chamadas (no_changes), com o head da
// última rodada e o CODEOWNERS do clone local: não sincroniza nem analisa o repo. Uma nova pausa
// fica gravada nela, como em qualquer rodada.
func (r *Runner) ResumePublish(ctx context.Context, repoName string) (Result, error) {
	if _, ok := r.Cfg.Repo(repoName); !ok {
		return Result{}, fmt.Errorf("repo %s não está no sentinela.yaml", repoName)
	}
	last, err := r.Store.Q.LastCompletedRun(ctx, repoName)
	if err != nil {
		return Result{}, fmt.Errorf("última rodada de %s: %w", repoName, err)
	}
	co, err := codeownersFrom(filepath.Join(r.Git.Root, filepath.FromSlash(repoName)))
	if err != nil {
		return Result{}, err
	}
	resolver := repo.NewResolver(r.Cfg, repo.StaticCodeowners{repoName: co})
	resolve := func(p string) (repo.Resolution, error) { return resolver.Resolve(repoName, p) }
	stats := Stats{Mode: "incremental", BaseSHA: last.HeadSha.String, HeadSHA: last.HeadSha.String, Provider: r.Cfg.LLM.Provider}
	if r.Cfg.LLM.Provider != config.ProviderAnthropicAPI {
		stats.MaxCalls = r.Cfg.LLM.ClaudeCode.MaxCallsPerRun
	}
	return r.noChanges(ctx, repoName, 0, stats, resolve, time.Now())
}
