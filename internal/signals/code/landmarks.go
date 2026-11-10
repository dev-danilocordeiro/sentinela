package code

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/dev-danilocordeiro/sentinela/internal/agents/architect"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
)

// Limites dos landmarks do Architect: o bastante para citar imports e declarações de cada
// pacote sem inflar a chamada única da rodada.
const (
	landmarksPerPackage = 40
	landmarksTotal      = 1500
	landmarkMaxFileSize = 256 << 10
	landmarkMaxChars    = 160
)

// declaration casa linhas de import e declaração nas linguagens mais comuns (Go, Java, Kotlin,
// Python, JS/TS) e em Dockerfile e nginx.
var declaration = regexp.MustCompile(`^(package\s|import\s|from\s+\S+\s+import\s|func\s|type\s|var\s|const\s|class\s|interface\s|enum\s|record\s|object\s|data class\s|def\s|async def\s|export\s|public\s|protected\s|private\s|internal\s|abstract\s|final\s|open class\s|@[A-Z]\w*|module\.exports|FROM\s|EXPOSE\s|ENTRYPOINT|CMD\s|location\s|proxy_pass\s|server\s*\{)`)

// yamlKey casa chaves YAML até o segundo nível (serviços e opções de um compose, por exemplo).
var yamlKey = regexp.MustCompile(`^( {0,4})[\w.-]+:`)

// imports casa as linhas de import; elas entram depois das declarações, porque um pacote com
// muitos imports esgotaria o limite antes de mostrar o que ele declara.
var imports = regexp.MustCompile(`^(import\s|from\s+\S+\s+import\s|package\s)`)

// landmarks extrai linhas reais do pacote que o Architect pode citar como evidência.
func landmarks(dir string, p repo.Package, budget int) []architect.Landmark {
	var decls, imps []architect.Landmark
	for _, f := range p.Files {
		if f.Size > landmarkMaxFileSize {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f.Path))) //nolint:gosec // arquivo do mapa de pacotes do checkout
		if err != nil {
			continue
		}
		ext := strings.ToLower(path.Ext(f.Path))
		yaml := ext == ".yml" || ext == ".yaml"
		for i, line := range strings.Split(string(data), "\n") {
			t := strings.TrimSpace(line)
			isMark := declaration.MatchString(t) || (yaml && yamlKey.MatchString(line))
			if t == "" || !isMark {
				continue
			}
			if r := []rune(t); len(r) > landmarkMaxChars {
				t = string(r[:landmarkMaxChars])
			}
			m := architect.Landmark{File: f.Path, Line: i + 1, Code: t}
			if imports.MatchString(t) {
				imps = append(imps, m)
			} else {
				decls = append(decls, m)
			}
		}
	}
	limit := min(landmarksPerPackage, budget)
	out := append(decls[:min(len(decls), limit)], imps[:min(len(imps), max(0, limit-len(decls)))]...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}
