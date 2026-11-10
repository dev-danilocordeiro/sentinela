package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
)

// unownedExamples é quantos caminhos sem time o aviso mostra.
const unownedExamples = 3

// unownedPathWarnings avisa os repos com caminhos que caem em defaults, sem time do Sentinela:
// sem repos[].team nem fallback_team, o que nenhum módulo nem o CODEOWNERS cobre (arquivos da raiz,
// CI, compose) fica sem dono. Com o clone do scan em discovery.workdir, confere arquivo por arquivo
// com o resolvedor; sem ele, avisa pela config.
func unownedPathWarnings(cfg *config.Config) []string {
	var out []string
	for i := range cfg.Repos {
		r := cfg.Repos[i]
		if r.Team != "" || r.FallbackTeam != "" || coversEverything(r.Modules) {
			continue
		}
		suggest := suggestedFallback(cfg, r)
		dir := filepath.Join(cfg.Discovery.Workdir, filepath.FromSlash(r.Name))
		if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
			out = append(out, fmt.Sprintf("%s: sem team nem fallback_team; os caminhos fora de modules e do CODEOWNERS (arquivos da raiz, CI, compose) caem em defaults, sem time. Defina repos[].fallback_team (ex.: fallback_team: %s) ou um módulo para eles", r.Name, suggest))
			continue
		}
		paths, err := unownedPaths(cfg, r.Name, dir)
		if err != nil {
			out = append(out, fmt.Sprintf("%s: não deu para conferir os caminhos sem time no clone %s: %v", r.Name, dir, err))
			continue
		}
		if len(paths) == 0 {
			continue
		}
		ex := paths[:min(len(paths), unownedExamples)]
		out = append(out, fmt.Sprintf("%s: %d arquivo(s) caem em defaults, sem time (ex.: %s). Defina repos[].fallback_team (ex.: fallback_team: %s) ou um módulo para eles; confira com `sentinela owners explain %s %s`", r.Name, len(paths), strings.Join(ex, ", "), suggest, r.Name, ex[0]))
	}
	return out
}

// coversEverything diz se um módulo com time casa qualquer caminho.
func coversEverything(mods []config.Module) bool {
	for _, m := range mods {
		if !m.Ignore && m.Team != "" && (m.Path == "**" || m.Path == "**/*") {
			return true
		}
	}
	return false
}

// suggestedFallback é o time do primeiro módulo do repo, ou o primeiro time da config.
func suggestedFallback(cfg *config.Config, r *config.Repo) string {
	for _, m := range r.Modules {
		if m.Team != "" {
			return m.Team
		}
	}
	if names := cfg.TeamNames(); len(names) > 0 {
		return names[0]
	}
	return "<time>"
}

// unownedPaths são os arquivos do clone que o resolvedor manda para defaults (sem os ignorados).
func unownedPaths(cfg *config.Config, name, dir string) ([]string, error) {
	co, err := cloneCodeowners(dir)
	if err != nil {
		return nil, err
	}
	resolver := repo.NewResolver(cfg, repo.StaticCodeowners{name: co})
	var out []string
	err = filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			switch e.Name() {
			case ".git", "node_modules", "vendor", "target", "build", "dist", ".next":
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		res, err := resolver.Resolve(name, rel)
		if err != nil {
			return err
		}
		if !res.Ignored && res.Rule == "defaults" {
			out = append(out, rel)
		}
		return nil
	})
	return out, err
}

// cloneCodeowners lê o CODEOWNERS do clone nos locais do GitHub; nil se não houver.
func cloneCodeowners(dir string) (*repo.Codeowners, error) {
	for _, p := range repo.CodeownersPaths {
		f, err := os.Open(filepath.Join(dir, filepath.FromSlash(p))) //nolint:gosec // caminhos fixos do GitHub
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		co, perr := repo.ParseCodeowners(f)
		_ = f.Close()
		return co, perr
	}
	return nil, nil
}
