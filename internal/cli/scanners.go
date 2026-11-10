package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/scan"
)

// discovery.scanners pelas linguagens dos repos (init, config bootstrap e o aviso do config
// validate): scan.ForLanguages escolhe os scanners; as linguagens vêm do clone do scan, quando ele
// existe, ou da API de linguagens do GitHub.

// scannerPlan é a lista discovery.scanners do sentinela.yaml gerado.
type scannerPlan struct {
	names   []string
	langs   map[string]bool // união das linguagens dos repos
	unknown []string        // repos sem linguagem detectada: a lista fica completa
}

// planScanners monta a lista pelas linguagens de cada repo (nil: não detectada).
func planScanners(repos []string, byRepo map[string]map[string]bool) scannerPlan {
	p := scannerPlan{langs: map[string]bool{}}
	for _, r := range repos {
		langs := byRepo[r]
		if langs == nil {
			p.unknown = append(p.unknown, r)
			continue
		}
		for l, ok := range langs {
			if ok && slices.Contains(scan.ScannerLanguages, l) {
				p.langs[l] = true
			}
		}
	}
	if len(repos) == 0 || len(p.unknown) > 0 {
		p.names = append([]string(nil), scan.AllScanners...)
	} else {
		p.names = scan.ForLanguages(p.langs)
	}
	return p
}

// yaml é a linha discovery.scanners, com o porquê e os pacotes do semgrep num comentário.
func (p scannerPlan) yaml() string {
	var why string
	switch {
	case len(p.unknown) > 0:
		why = "linguagens não detectadas em " + strings.Join(p.unknown, ", ") + ": lista completa; os que não se aplicam aparecem como pulados"
	case len(p.names) == len(scan.AllScanners) && len(p.langs) == 0:
		why = "sem repos: lista completa; os que não se aplicam aparecem como pulados"
	default:
		why = "pelas linguagens dos repos: " + orDash(scan.LanguageList(p.langs))
	}
	out := "  scanners: [" + strings.Join(p.names, ", ") + "]   # " + why + "\n"
	if packs := semgrepPacks(p.langs); packs != "" {
		out += "  # semgrep: p/default e " + packs + " (discovery.semgrep_languages, padrão)\n"
	}
	return out
}

// semgrepPacks lista os pacotes de regras do semgrep das linguagens, sem repetir.
func semgrepPacks(langs map[string]bool) string {
	var names []string
	for l := range langs {
		if _, ok := config.DefaultSemgrepLanguages[l]; ok {
			names = append(names, l)
		}
	}
	slices.Sort(names)
	var packs []string
	for _, l := range names {
		for _, c := range config.DefaultSemgrepLanguages[l] {
			if !slices.Contains(packs, c) {
				packs = append(packs, c)
			}
		}
	}
	return strings.Join(packs, ", ")
}

// cloneLanguages lê as linguagens do clone do scan (workdir/owner/repo), sem rede; nil sem clone.
func cloneLanguages(workdir, fullName string) map[string]bool {
	dir := filepath.Join(workdir, filepath.FromSlash(fullName))
	if st, err := os.Stat(filepath.Join(dir, ".git")); err != nil || !st.IsDir() {
		return nil
	}
	pkgs, err := repo.MapPackages(dir)
	if err != nil {
		return nil
	}
	langs := map[string]bool{}
	for _, p := range pkgs {
		langs[p.Language] = true
	}
	return langs
}

// ghLanguages pergunta ao GitHub, pelo gh, as linguagens do repo (GET /repos/{repo}/languages).
func ghLanguages(ctx context.Context, run func(context.Context, string, ...string) (string, error), fullName string) (map[string]bool, error) {
	out, err := run(ctx, "gh", "api", "repos/"+fullName+"/languages")
	if err != nil {
		return nil, fmt.Errorf("gh api repos/%s/languages: %w: %s", fullName, err, out)
	}
	var bytes map[string]int64
	if err := json.Unmarshal([]byte(out), &bytes); err != nil {
		return nil, fmt.Errorf("gh api repos/%s/languages: %w", fullName, err)
	}
	return scan.LanguagesFromGitHub(bytes), nil
}

// scannerCoverageWarnings avisa, para cada repo com clone do scan, das linguagens cujo scanner está
// instalado nesta máquina e fora de discovery.scanners. Sem clone, o repo não é conferido.
func scannerCoverageWarnings(cfg *config.Config, look func(string) (string, error)) []string {
	workdir, err := filepath.Abs(cfg.Discovery.Workdir)
	if err != nil {
		return nil
	}
	var out []string
	for _, r := range cfg.Repos {
		langs := cloneLanguages(workdir, r.Name)
		if langs == nil {
			continue
		}
		var missing []string
		var why []string
		for _, s := range scan.AllScanners {
			by, ok := scan.ScannersOf(langs)[s]
			if !ok || slices.Contains(cfg.Discovery.Scanners, s) || !scan.Installed(s, cfg.Discovery.NodeTools, look) {
				continue
			}
			missing = append(missing, s)
			for _, l := range by {
				if !slices.Contains(why, l) {
					why = append(why, l)
				}
			}
		}
		if len(missing) > 0 {
			out = append(out, fmt.Sprintf("%s tem %s (clone do último scan): %s instalado(s) e fora de discovery.scanners", r.Name, strings.Join(why, ", "), strings.Join(missing, ", ")))
		}
	}
	return out
}
