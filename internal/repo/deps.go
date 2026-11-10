package repo

import (
	"bufio"
	"bytes"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Edge é uma dependência entre dois pacotes do mesmo repo.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

var (
	goImport     = regexp.MustCompile(`^\s*(?:import\s+)?(?:[\w.]+\s+)?"([^"]+)"`)
	jvmImport    = regexp.MustCompile(`^\s*import\s+(?:static\s+)?([\w.]+(?:\.\*)?)\s*;?\s*$`)
	jsImport     = regexp.MustCompile(`(?:from\s+|require\(\s*|import\s*\(\s*|import\s+)['"]([^'"\s]+)['"]`)
	pyFromImport = regexp.MustCompile(`^\s*from\s+([\w.]+)\s+import\s`)
	pyImport     = regexp.MustCompile(`^\s*import\s+([\w.]+)`)
	goModule     = regexp.MustCompile(`(?m)^module\s+(\S+)`)
)

// DependencyGraph extrai imports internos por linguagem e devolve as arestas entre
// pacotes do repo. É uma aproximação léxica: suficiente para o System Architect ver
// a forma do sistema, sem compilar nada. Em TypeScript e JavaScript, os imports não relativos
// passam pelo tsconfig (paths e baseUrl) e pelos nomes dos pacotes de workspace (tsgraph.go).
func DependencyGraph(root string, pkgs []Package) []Edge {
	byPath := map[string]bool{}
	for _, p := range pkgs {
		byPath[p.Path] = true
	}
	// Pacotes JVM se identificam pelo sufixo do diretório (com/acme/x).
	jvmBySuffix := map[string]string{}
	for _, p := range pkgs {
		if p.Language == "java" || p.Language == "kotlin" {
			if i := strings.Index(p.Path, "/java/"); i >= 0 {
				jvmBySuffix[p.Path[i+len("/java/"):]] = p.Path
			} else if i := strings.Index(p.Path, "/kotlin/"); i >= 0 {
				jvmBySuffix[p.Path[i+len("/kotlin/"):]] = p.Path
			}
		}
	}
	module := ""
	if data, err := ReadFile(root, "go.mod"); err == nil {
		if m := goModule.FindSubmatch(data); m != nil {
			module = string(m[1])
		}
	}

	var ts *tsResolver // só lido quando o repo tem arquivo de TS ou JS
	seen := map[Edge]bool{}
	for _, p := range pkgs {
		for _, f := range p.Files {
			data, err := ReadFile(root, f.Path)
			if err != nil {
				continue
			}
			if ts == nil && isJSFile(f.Path) {
				ts = newTSResolver(root)
			}
			for _, target := range importsOf(f.Path, data, module, jvmBySuffix, ts) {
				if target == p.Path || !byPath[target] {
					continue
				}
				seen[Edge{From: p.Path, To: target}] = true
			}
		}
	}
	edges := make([]Edge, 0, len(seen))
	for e := range seen {
		edges = append(edges, e)
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})
	return edges
}

func isJSFile(file string) bool {
	switch path.Ext(file) {
	case ".ts", ".tsx", ".js", ".jsx", ".mjs":
		return true
	}
	return false
}

func importsOf(file string, data []byte, goMod string, jvm map[string]string, ts *tsResolver) []string {
	var out []string
	dir := path.Dir(file)
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch path.Ext(file) {
		case ".go":
			if m := goImport.FindStringSubmatch(line); m != nil && goMod != "" && strings.HasPrefix(m[1], goMod+"/") {
				out = append(out, strings.TrimPrefix(m[1], goMod+"/"))
			}
		case ".java", ".kt", ".kts", ".scala":
			if m := jvmImport.FindStringSubmatch(line); m != nil {
				if target, ok := jvm[jvmPackage(m[1])]; ok {
					out = append(out, target)
				}
			}
		case ".ts", ".tsx", ".js", ".jsx", ".mjs":
			for _, m := range jsImport.FindAllStringSubmatch(line, -1) {
				if p := ts.resolve(file, m[1]); p != "" {
					out = append(out, ts.packageOf(p))
				}
			}
		case ".py":
			if m := pyFromImport.FindStringSubmatch(line); m != nil {
				out = append(out, pyModuleDir(m[1], dir))
			} else if m := pyImport.FindStringSubmatch(line); m != nil {
				out = append(out, pyModuleDir(m[1], dir))
			}
		}
	}
	return out
}

func pyModuleDir(mod, dir string) string {
	if strings.HasPrefix(mod, ".") {
		up := len(mod) - len(strings.TrimLeft(mod, "."))
		base := dir
		for i := 1; i < up; i++ {
			base = path.Dir(base)
		}
		rest := strings.TrimLeft(mod, ".")
		if rest == "" {
			return base
		}
		return path.Join(base, path.Dir(strings.ReplaceAll(rest, ".", "/")+"/x"))
	}
	return path.Dir(strings.ReplaceAll(mod, ".", "/") + ".py")
}

// jvmPackage corta o import no primeiro segmento que começa com maiúscula (a classe):
// com.acme.model.UrlData.of -> com/acme/model; com.acme.model.* -> com/acme/model.
func jvmPackage(imp string) string {
	var segs []string
	for _, s := range strings.Split(imp, ".") {
		if s == "*" || (s != "" && s[0] >= 'A' && s[0] <= 'Z') {
			break
		}
		segs = append(segs, s)
	}
	return strings.Join(segs, "/")
}
