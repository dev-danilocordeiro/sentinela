package repo

import (
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Package é a unidade de análise: um diretório com arquivos de código ou configuração.
// Funciona para qualquer linguagem (pacote Go, pacote Java, diretório TS, módulo Python).
type Package struct {
	Path     string // diretório relativo à raiz, com "/"; "." para a raiz
	Language string // linguagem predominante
	Files    []File
}

type File struct {
	Path string // relativo à raiz, com "/"
	Size int64
}

// Bytes soma o tamanho dos arquivos do pacote.
func (p Package) Bytes() int64 {
	var n int64
	for _, f := range p.Files {
		n += f.Size
	}
	return n
}

var languageByExt = map[string]string{
	".go": "go", ".java": "java", ".kt": "kotlin", ".kts": "kotlin", ".py": "python",
	".ts": "typescript", ".tsx": "typescript", ".js": "javascript", ".jsx": "javascript",
	".mjs": "javascript", ".rb": "ruby", ".rs": "rust", ".cs": "csharp", ".php": "php",
	".scala": "scala", ".swift": "swift", ".lua": "lua", ".sql": "sql", ".html": "html",
	".yaml": "config", ".yml": "config", ".properties": "config", ".toml": "config",
}

// Arquivos de configuração sem extensão conhecida que costumam esconder problemas reais.
var configNames = map[string]bool{"Dockerfile": true, "docker-compose.yml": true, "docker-compose.yaml": true, "nginx.conf": true}

var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "target": true, "build": true, "dist": true,
	".mvn": true, ".idea": true, ".vscode": true, ".gradle": true, "__pycache__": true, ".venv": true,
	"venv": true, "coverage": true, ".next": true, ".angular": true, "testdata": true, "bin": true, "obj": true,
}

// MaxFileBytes descarta arquivos grandes demais para análise (lockfiles, gerados, dados).
const MaxFileBytes = 200_000

// MapPackages percorre o checkout e agrupa arquivos de código por diretório.
// Testes ficam de fora: eles entram como sinal pelos scanners, não como alvo de análise.
func MapPackages(root string) ([]Package, error) {
	byDir := map[string]*Package{}
	langCount := map[string]map[string]int{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && (skipDirs[name] || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		lang, ok := languageByExt[strings.ToLower(path.Ext(name))]
		if !ok && configNames[name] {
			lang, ok = "config", true
		}
		if !ok || IsTestFile(rel) || isGenerated(name) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() == 0 || info.Size() > MaxFileBytes {
			return nil
		}
		dir := path.Dir(rel)
		pkg := byDir[dir]
		if pkg == nil {
			pkg = &Package{Path: dir}
			byDir[dir] = pkg
			langCount[dir] = map[string]int{}
		}
		pkg.Files = append(pkg.Files, File{Path: rel, Size: info.Size()})
		langCount[dir][lang]++
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]Package, 0, len(byDir))
	for dir, pkg := range byDir {
		pkg.Language = dominant(langCount[dir])
		sort.Slice(pkg.Files, func(i, j int) bool { return pkg.Files[i].Path < pkg.Files[j].Path })
		out = append(out, *pkg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// IsTestFile reconhece testes pelas convenções mais comuns das linguagens suportadas.
func IsTestFile(rel string) bool {
	base := path.Base(rel)
	switch {
	case strings.HasSuffix(base, "_test.go"),
		strings.HasSuffix(base, "Test.java"), strings.HasSuffix(base, "Tests.java"), strings.HasSuffix(base, "IT.java"),
		strings.HasSuffix(base, "Test.kt"), strings.HasSuffix(base, "Tests.kt"), strings.HasSuffix(base, "IT.kt"),
		strings.HasSuffix(base, "Spec.kt"),
		strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py"),
		strings.Contains(base, ".spec."), strings.Contains(base, ".test."):
		return true
	}
	for _, seg := range strings.Split(path.Dir(rel), "/") {
		if seg == "test" || seg == "tests" || seg == "__tests__" || seg == "e2e" {
			return true
		}
	}
	return false
}

func isGenerated(name string) bool {
	switch name {
	case "package-lock.json", "pnpm-lock.yaml", "yarn.lock":
		return true
	}
	return strings.HasSuffix(name, ".pb.go") || strings.HasSuffix(name, "_gen.go") ||
		strings.HasSuffix(name, ".min.js") || strings.HasSuffix(name, ".d.ts") ||
		strings.HasSuffix(name, ".generated.ts") || strings.HasSuffix(name, ".generated.tsx")
}

func dominant(counts map[string]int) string {
	best, bestN := "", math.MinInt
	for lang, n := range counts {
		// Código vence configuração no empate e em minoria.
		if lang == "config" {
			n = n/2 - 1
		}
		if n > bestN || (n == bestN && lang < best) {
			best, bestN = lang, n
		}
	}
	return best
}

// FilterChanged mantém só os pacotes que contêm algum dos arquivos alterados.
func FilterChanged(pkgs []Package, changed []string) []Package {
	dirs := map[string]bool{}
	for _, f := range changed {
		dirs[path.Dir(filepath.ToSlash(f))] = true
	}
	var out []Package
	for _, p := range pkgs {
		if dirs[p.Path] {
			out = append(out, p)
		}
	}
	return out
}

// ReadFile lê um arquivo do checkout.
func ReadFile(root, rel string) ([]byte, error) {
	return os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))) //nolint:gosec // rel vem do MapPackages
}
