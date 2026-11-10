package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
)

// configAt monta, num diretório temporário, a config como estava no commit base: o arquivo
// principal (se não estiver no git, como numa instalação com o sentinela.yaml fora dele, usa o
// atual) e os config.d/teams/*.yaml da base. Devolve nil se ela não carregar.
func configAt(ctx context.Context, cfgPath, base, tmp string) (*config.Config, error) {
	abs, err := filepath.Abs(cfgPath)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(abs)
	top, err := gitOut(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("--changed precisa de um repo git: %w", err)
	}
	relMain, _ := filepath.Rel(top, abs)
	relTeams, _ := filepath.Rel(top, filepath.Join(dir, filepath.FromSlash(config.TeamFilesDir)))
	main, err := gitOut(ctx, top, "show", base+":"+filepath.ToSlash(relMain))
	if err != nil {
		b, rerr := os.ReadFile(abs) //nolint:gosec // o arquivo da própria config
		if rerr != nil {
			return nil, rerr
		}
		main = string(b)
	}
	if err := os.MkdirAll(filepath.Join(tmp, filepath.FromSlash(config.TeamFilesDir)), 0o750); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(tmp, "sentinela.yaml"), []byte(main+"\n"), 0o600); err != nil { //nolint:gosec // diretório temporário criado pelo próprio comando

		return nil, err
	}
	names, _ := gitOut(ctx, top, "ls-tree", "--name-only", base, filepath.ToSlash(relTeams)+"/")
	for _, n := range strings.Fields(names) {
		if !strings.HasSuffix(n, ".yaml") {
			continue
		}
		body, err := gitOut(ctx, top, "show", base+":"+n)
		if err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(tmp, filepath.FromSlash(config.TeamFilesDir), filepath.Base(n)), []byte(body+"\n"), 0o600); err != nil {
			return nil, err
		}
	}
	return config.Load(filepath.Join(tmp, "sentinela.yaml"))
}

func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output() //nolint:gosec // comandos fixos do git
	return strings.TrimSpace(string(out)), err
}

// samplePath transforma o glob de um módulo num caminho de exemplo que casa com ele.
func samplePath(glob string) string {
	p := strings.ReplaceAll(glob, "**", "exemplo")
	p = strings.ReplaceAll(p, "*", "exemplo")
	if strings.HasSuffix(p, "/") || p == "" {
		p += "arquivo"
	}
	return strings.TrimPrefix(p, "/")
}

type affected struct{ repo, path string }

// affectedPaths são os caminhos que os arquivos mudados podem rotear de outro jeito: os módulos
// e repos dos arquivos de time mudados (antes e depois); se o principal mudou, todos.
func affectedPaths(changed []string, cfgs ...*config.Config) []affected {
	all := false
	teams := map[string]bool{}
	for _, f := range changed {
		f = filepath.ToSlash(f)
		switch {
		case strings.Contains(f, config.TeamFilesDir+"/") && strings.HasSuffix(f, ".yaml"):
			teams[strings.TrimSuffix(filepath.Base(f), ".yaml")] = true
		case strings.HasSuffix(f, ".yaml"):
			all = true
		}
	}
	var out []affected
	addOnce := func(a affected) {
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	for _, c := range cfgs {
		if c == nil {
			continue
		}
		for _, r := range c.Repos {
			if all || teams[r.Team] {
				addOnce(affected{r.Name, "arquivo-na-raiz.go"})
			}
			for _, m := range r.Modules {
				if all || teams[m.Team] {
					addOnce(affected{r.Name, samplePath(m.Path)})
				}
			}
		}
	}
	return out
}

func explainLine(c *config.Config, co repo.CodeownersSource, a affected) string {
	if c == nil {
		return "(config inválida ou ausente)"
	}
	if _, ok := c.Repo(a.repo); !ok {
		return "(repo fora da config)"
	}
	res, err := repo.NewResolver(c, co).Resolve(a.repo, a.path)
	if err != nil {
		return "erro: " + err.Error()
	}
	if res.Ignored {
		return "ignorado (" + res.Rule + ")"
	}
	return fmt.Sprintf("time %s [%s] · projeto %s [%s] · canal %s [%s] · menção %s [%s]",
		orDash(res.Team), res.Rule, orDash(res.LinearProject), res.ProjectSource, orDash(res.NotifySlack), res.NotifySource, orDash(res.Mention), res.MentionSource)
}

// changedReport compara o roteamento dos caminhos afetados antes (base) e depois (arquivos atuais).
func changedReport(ctx context.Context, cfgPath string, after *config.Config, changed []string, base string, co repo.CodeownersSource) ([]string, error) {
	tmp, err := os.MkdirTemp("", "sentinela-base-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	before, berr := configAt(ctx, cfgPath, base, tmp)
	var out []string
	if berr != nil {
		out = append(out, fmt.Sprintf("antes (%s): a config não carrega: %v", base, oneLine(berr)))
		before = nil
	}
	paths := affectedPaths(changed, before, after)
	if len(paths) == 0 {
		return append(out, "nenhum caminho afetado pelos arquivos mudados"), nil
	}
	out = append(out, fmt.Sprintf("owners explain dos caminhos afetados (antes = %s, depois = arquivos atuais):", base))
	for _, a := range paths {
		b, d := explainLine(before, co, a), explainLine(after, co, a)
		if b == d {
			out = append(out, fmt.Sprintf("  %s %s: sem mudança: %s", a.repo, a.path, d))
			continue
		}
		out = append(out, fmt.Sprintf("  %s %s:\n    antes:  %s\n    depois: %s", a.repo, a.path, b, d))
	}
	return out, nil
}

func oneLine(err error) string { return strings.ReplaceAll(err.Error(), "\n", "; ") }
