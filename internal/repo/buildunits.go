package repo

import (
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"
)

// Unidades de build (Fase C, passo 3): os módulos Go, Gradle e Maven e os pacotes npm, yarn e
// pnpm do repo, lidos só dos manifestos, sem rodar o build (que executaria código do repo). Os
// scanners de uma linguagem rodam uma vez por unidade dela, como o perGoModule fazia com go.mod.

// Tipos de unidade.
const (
	KindGo     = "go"
	KindGradle = "gradle"
	KindMaven  = "maven"
	KindNPM    = "npm"
	KindYarn   = "yarn"
	KindPNPM   = "pnpm"
)

// BuildUnit é um módulo ou pacote: o diretório (relativo, com "/"; "." é a raiz) e o tipo.
type BuildUnit struct {
	Dir  string `json:"dir"`
	Kind string `json:"kind"`
}

// JS diz se a unidade é um pacote JavaScript ou TypeScript (npm, yarn ou pnpm).
func (u BuildUnit) JS() bool { return u.Kind == KindNPM || u.Kind == KindYarn || u.Kind == KindPNPM }

// JVM diz se a unidade é um módulo Gradle ou Maven.
func (u BuildUnit) JVM() bool { return u.Kind == KindGradle || u.Kind == KindMaven }

// unitSkipDirs não são varridos: dependências baixadas, saídas de build e fixtures.
var unitSkipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "testdata": true, "build": true, "target": true,
	"dist": true, "out": true, ".next": true, "bower_components": true,
}

var (
	gradleInclude = regexp.MustCompile(`^\s*include\b(.*)$`)
	quoted        = regexp.MustCompile(`["']([^"']+)["']`)
)

// BuildUnits lista as unidades de build do checkout, em ordem de diretório e tipo.
func BuildUnits(root string) []BuildUnit {
	found := map[BuildUnit]bool{}
	var pkgDirs []string                // diretórios com package.json
	workspaces := map[string][]string{} // raiz do workspace -> globs (com ! para excluir)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // diretório ilegível não impede achar as outras unidades
		}
		if d.IsDir() {
			if p != root && (unitSkipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		dir := rel(root, filepath.Dir(p))
		switch d.Name() {
		case "go.mod":
			found[BuildUnit{dir, KindGo}] = true
		case "pom.xml":
			found[BuildUnit{dir, KindMaven}] = true
		case "build.gradle", "build.gradle.kts":
			found[BuildUnit{dir, KindGradle}] = true
		case "settings.gradle", "settings.gradle.kts":
			found[BuildUnit{dir, KindGradle}] = true
			for _, inc := range gradleIncludes(p) {
				sub := path.Join(dir, inc)
				if fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(sub))); err == nil && fi.IsDir() {
					found[BuildUnit{sub, KindGradle}] = true
				}
			}
		case "package.json":
			pkgDirs = append(pkgDirs, dir)
			if globs := packageWorkspaces(p); len(globs) > 0 {
				workspaces[dir] = append(workspaces[dir], globs...)
			}
		case "pnpm-workspace.yaml":
			workspaces[dir] = append(workspaces[dir], pnpmWorkspaces(p)...)
		}
		return nil
	})
	for _, dir := range pkgDirs {
		wsRoot, inWorkspace := workspaceOf(dir, workspaces)
		if inWorkspace && dir != wsRoot && !workspaceMember(dir, wsRoot, workspaces[wsRoot]) {
			continue
		}
		lockDir := dir
		if inWorkspace {
			lockDir = wsRoot
		}
		found[BuildUnit{dir, jsManager(root, lockDir)}] = true
	}
	out := make([]BuildUnit, 0, len(found))
	for u := range found {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir < out[j].Dir
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// UnitsOf filtra as unidades por tipo.
func UnitsOf(units []BuildUnit, keep func(BuildUnit) bool) []BuildUnit {
	var out []BuildUnit
	for _, u := range units {
		if keep(u) {
			out = append(out, u)
		}
	}
	return out
}

// gradleIncludes lê os projetos de include(...) ou include '...' do settings, ignorando
// comentários de linha; ":a:b" vira "a/b".
func gradleIncludes(file string) []string {
	data, err := os.ReadFile(file) //nolint:gosec // arquivo do checkout
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		m := gradleInclude.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		for _, q := range quoted.FindAllStringSubmatch(m[1], -1) {
			name := strings.Trim(strings.ReplaceAll(q[1], ":", "/"), "/")
			if name != "" && !strings.Contains(name, "..") {
				out = append(out, name)
			}
		}
	}
	return out
}

// packageWorkspaces lê "workspaces" do package.json: lista de globs ou {packages: [...]}.
func packageWorkspaces(file string) []string {
	data, err := os.ReadFile(file) //nolint:gosec // arquivo do checkout
	if err != nil {
		return nil
	}
	var pkg struct {
		Workspaces json.RawMessage `json:"workspaces"`
	}
	if json.Unmarshal(data, &pkg) != nil || len(pkg.Workspaces) == 0 {
		return nil
	}
	var list []string
	if json.Unmarshal(pkg.Workspaces, &list) == nil {
		return list
	}
	var obj struct {
		Packages []string `json:"packages"`
	}
	if json.Unmarshal(pkg.Workspaces, &obj) == nil {
		return obj.Packages
	}
	return nil
}

// pnpmWorkspaces lê packages do pnpm-workspace.yaml.
func pnpmWorkspaces(file string) []string {
	data, err := os.ReadFile(file) //nolint:gosec // arquivo do checkout
	if err != nil {
		return nil
	}
	var ws struct {
		Packages []string `yaml:"packages"`
	}
	if yaml.Unmarshal(data, &ws) != nil {
		return nil
	}
	return ws.Packages
}

// workspaceOf acha a raiz de workspace mais próxima que contém dir (ele mesmo incluído).
func workspaceOf(dir string, workspaces map[string][]string) (string, bool) {
	for d := dir; ; d = path.Dir(d) {
		if _, ok := workspaces[d]; ok {
			return d, true
		}
		if d == "." || d == "/" {
			return "", false
		}
	}
}

// workspaceMember diz se dir casa algum glob do workspace e nenhum de exclusão (!glob).
func workspaceMember(dir, wsRoot string, globs []string) bool {
	relDir := strings.TrimPrefix(strings.TrimPrefix(dir, wsRoot), "/")
	if wsRoot == "." {
		relDir = dir
	}
	member := false
	for _, g := range globs {
		neg := strings.HasPrefix(g, "!")
		g = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(g, "!"), "./"), "/")
		if ok, _ := doublestar.Match(g, relDir); ok {
			if neg {
				return false
			}
			member = true
		}
	}
	return member
}

// jsManager escolhe o gerenciador pelo lockfile do diretório (pnpm, yarn; senão npm).
func jsManager(root, dir string) string {
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir), name))
		return err == nil
	}
	switch {
	case has("pnpm-lock.yaml"), has("pnpm-workspace.yaml"):
		return KindPNPM
	case has("yarn.lock"):
		return KindYarn
	}
	return KindNPM
}

// rel devolve p relativo a root, com "/" ("." para a raiz).
func rel(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(r)
}

// UnitsFor devolve, para cada caminho do escopo (diretório, arquivo ou glob), a unidade de build
// mais funda que o contém, na ordem do escopo e sem repetir. Num monorepo de front e BFF, é o
// workspace que o job testa.
func UnitsFor(root string, scope []string) []BuildUnit {
	units := BuildUnits(root)
	var out []BuildUnit
	for _, s := range scope {
		start := scopeDir(root, s)
		if start == "" {
			continue
		}
		best, bestDepth := BuildUnit{}, -1
		for _, u := range units {
			if u.Dir != "." && start != u.Dir && !strings.HasPrefix(start, u.Dir+"/") {
				continue
			}
			// A mais funda vence; no mesmo diretório (go.mod e package.json), fica a primeira.
			if d := unitDepth(u.Dir); d > bestDepth {
				best, bestDepth = u, d
			}
		}
		found := bestDepth >= 0
		if found && !slices.Contains(out, best) {
			out = append(out, best)
		}
	}
	return out
}

func unitDepth(dir string) int {
	if dir == "." {
		return 0
	}
	return strings.Count(dir, "/") + 1
}

// PackageName lê o name do package.json da unidade JS ("" sem nome ou sem arquivo).
func PackageName(root string, u BuildUnit) string {
	if !u.JS() {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(u.Dir), "package.json")) //nolint:gosec // arquivo do checkout
	if err != nil {
		return ""
	}
	var pkg struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(data, &pkg)
	return pkg.Name
}
