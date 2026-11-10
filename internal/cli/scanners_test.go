package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/scan"
)

// fixtureLanguages são as linguagens do testdata/ts-monorepo, como o clone do scan as daria.
func fixtureLanguages(t *testing.T) map[string]bool {
	t.Helper()
	pkgs, err := repo.MapPackages(filepath.Join("..", "..", "testdata", "ts-monorepo"))
	if err != nil {
		t.Fatal(err)
	}
	langs := map[string]bool{}
	for _, p := range pkgs {
		langs[p.Language] = true
	}
	return langs
}

// O sentinela.yaml do init sai com os scanners das linguagens dos repos: Go, o monorepo TS e um
// repo misto; a lista completa só quando alguma linguagem não foi detectada.
func TestInitConfigScannersFollowTheLanguagesOfTheRepos(t *testing.T) {
	prof, _ := config.Profile(config.DefaultProfile)
	goRepo := scan.LanguagesFromGitHub(map[string]int64{"Go": 120_000, "Makefile": 900})
	ts := fixtureLanguages(t)
	for _, c := range []struct {
		name   string
		repos  []string
		langs  map[string]map[string]bool
		want   []string
		line   string
		semgre string
	}{
		{"repo Go", []string{"acme/svc"}, map[string]map[string]bool{"acme/svc": goRepo},
			[]string{"golangci-lint", "gosec", "govulncheck", "semgrep"},
			"  scanners: [golangci-lint, gosec, govulncheck, semgrep]   # pelas linguagens dos repos: go\n", ""},
		{"testdata/ts-monorepo", []string{"acme/loja"}, map[string]map[string]bool{"acme/loja": ts},
			[]string{"semgrep", "osv-scanner", "eslint", "tsc"},
			"  scanners: [semgrep, osv-scanner, eslint, tsc]   # pelas linguagens dos repos: typescript\n",
			"  # semgrep: p/default e p/typescript, p/react, p/nodejs, p/expressjs (discovery.semgrep_languages, padrão)\n"},
		{"Go e TS", []string{"acme/svc", "acme/loja"}, map[string]map[string]bool{"acme/svc": goRepo, "acme/loja": ts},
			[]string{"golangci-lint", "gosec", "govulncheck", "semgrep", "osv-scanner", "eslint", "tsc"},
			"  scanners: [golangci-lint, gosec, govulncheck, semgrep, osv-scanner, eslint, tsc]   # pelas linguagens dos repos: go, typescript\n", ""},
		{"linguagem não detectada", []string{"acme/svc", "acme/x"}, map[string]map[string]bool{"acme/svc": goRepo},
			scan.AllScanners, "linguagens não detectadas em acme/x: lista completa", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			for mode, y := range map[string]string{
				"local": localConfig(c.repos, prof, planScanners(c.repos, c.langs)),
				"mcp":   mcpConfig(c.repos, "HAY", "", prof, planScanners(c.repos, c.langs)),
			} {
				cfg, err := config.Parse([]byte(y))
				if err != nil {
					t.Fatalf("%s: %v", mode, err)
				}
				if !slices.Equal(cfg.Discovery.Scanners, c.want) {
					t.Errorf("%s: scanners = %v, quer %v", mode, cfg.Discovery.Scanners, c.want)
				}
				if !strings.Contains(y, c.line) || (c.semgre != "" && !strings.Contains(y, c.semgre)) {
					t.Errorf("%s: yaml sem %q / %q:\n%s", mode, c.line, c.semgre, discoveryBlock(y))
				}
			}
		})
	}
}

func discoveryBlock(y string) string {
	i := strings.Index(y, "discovery:")
	j := strings.Index(y[i:], "\n\n")
	return y[i : i+j]
}

// O init pergunta as linguagens ao GitHub (gh api) quando não há clone do scan; sem resposta, a
// lista fica completa e o init avisa.
func TestInitAsksGitHubForTheLanguagesOfEachRepo(t *testing.T) {
	for _, c := range []struct {
		name string
		resp map[string]map[string]bool
		want []string
		out  string
	}{
		{"Go e TS", map[string]map[string]bool{"acme/api": {"go": true}, "acme/web": {"typescript": true}},
			[]string{"golangci-lint", "gosec", "govulncheck", "semgrep", "osv-scanner", "eslint", "tsc"},
			"ok: linguagens de acme/web: typescript (GitHub)"},
		{"sem resposta para um repo", map[string]map[string]bool{"acme/api": {"go": true}},
			scan.AllScanners, "aviso: linguagens de acme/web não detectadas (repo privado): discovery.scanners sai com a lista completa"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if err := os.WriteFile("docker-compose.yml", []byte("services: {}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			f := &fakeInit{}
			h := f.hooks()
			h.Languages = func(_ context.Context, name string) (map[string]bool, error) {
				if l, ok := c.resp[name]; ok {
					return l, nil
				}
				return nil, errors.New("repo privado")
			}
			out, err := runWith(t, map[string]string{}, h, "--config", "sentinela.yaml", "init", "--mode", "local", "--repo", "acme/api", "--repo", "acme/web")
			if err != nil {
				t.Fatalf("init: %v\n%s", err, out)
			}
			cfg, err := config.Load("sentinela.yaml")
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(cfg.Discovery.Scanners, c.want) || !strings.Contains(out, c.out) {
				t.Errorf("scanners = %v, quer %v; saída:\n%s", cfg.Discovery.Scanners, c.want, out)
			}
		})
	}
}

// O config validate avisa quando o clone de um repo tem uma linguagem cujo scanner está instalado
// e fora de discovery.scanners; o que não está instalado não entra no aviso.
func TestConfigValidateWarnsAboutInstalledScannersOutOfTheList(t *testing.T) {
	dir := t.TempDir()
	work := filepath.Join(dir, "repos")
	tools := filepath.Join(dir, "tools")
	for rel, content := range map[string]string{
		"repos/acme/web/.git/HEAD":                "ref: refs/heads/main\n",
		"repos/acme/web/package.json":             `{"name": "web"}`,
		"repos/acme/web/src/a.ts":                 "export const a = 1;\n",
		"repos/acme/svc/.git/HEAD":                "ref: refs/heads/main\n",
		"repos/acme/svc/main.go":                  "package main\n",
		"tools/node_modules/eslint/bin/eslint.js": "x",
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o750)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	y := strings.Replace(LocalConfig([]string{"acme/web", "acme/svc"}), "\ndiscovery:\n",
		"\ndiscovery:\n  workdir: "+work+"\n  node_tools: "+tools+"\n", 1)
	y = strings.Replace(y, "  scanners: ["+strings.Join(scan.AllScanners, ", ")+"]", "  scanners: [golangci-lint, semgrep]", 1)
	path := filepath.Join(dir, "sentinela.yaml")
	if err := os.WriteFile(path, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeInit{missing: map[string]bool{"osv-scanner": true, "gosec": true}}
	out, err := runWith(t, map[string]string{}, f.hooks(), "--config", path, "config", "validate", "--offline")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"aviso: acme/web tem typescript (clone do último scan): eslint instalado(s) e fora de discovery.scanners",
		"aviso: acme/svc tem go (clone do último scan): govulncheck instalado(s) e fora de discovery.scanners",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("saída sem %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "osv-scanner instalado") || strings.Contains(out, "tsc") || strings.Contains(out, "gosec") {
		t.Errorf("scanner não instalado entrou no aviso:\n%s", out)
	}
}
