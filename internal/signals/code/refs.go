package code

import (
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"
)

// qualified casa identificadores Go qualificados por pacote: turmas.ErrNaoEncontrada.
var qualified = regexp.MustCompile(`\b([a-z][a-zA-Z0-9_]*)\.([A-Z][a-zA-Z0-9_]*)`)

// goRefs devolve os diretórios, relativos ao repo, dos pacotes Go do próprio repo que os textos
// citam como pkg.Ident (correção proposta e snippets), resolvendo pkg pelos imports dos arquivos
// .go da evidência. É a terceira fonte do roteamento por achado (ADR 0017): na EX-15, a evidência
// e o escopo eram só go/cmd/api, mas a correção pede turmas.ErrNaoEncontrada, de go/internal/turmas.
func goRefs(dir string, files []string, texts ...string) []string {
	aliases := map[string]string{} // nome do pacote no arquivo -> diretório no repo
	for _, f := range files {
		if !strings.HasSuffix(f, ".go") {
			continue
		}
		modDir, modPath := goModuleOf(dir, path.Dir(f))
		if modPath == "" {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, filepath.FromSlash(f)), nil, parser.ImportsOnly)
		if err != nil {
			continue
		}
		for _, imp := range file.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil || !strings.HasPrefix(p, modPath+"/") {
				continue // biblioteca padrão ou dependência externa
			}
			name := path.Base(p)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			aliases[name] = path.Join(modDir, strings.TrimPrefix(p, modPath+"/"))
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, t := range texts {
		for _, m := range qualified.FindAllStringSubmatch(t, -1) {
			if d, ok := aliases[m[1]]; ok && !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
		}
	}
	return out
}

// goModuleOf sobe de rel até a raiz do repo procurando go.mod; devolve o diretório do módulo
// (relativo, "." na raiz) e o module path.
func goModuleOf(dir, rel string) (string, string) {
	for d := rel; ; d = path.Dir(d) {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(d), "go.mod")) //nolint:gosec // go.mod do checkout
		if err == nil {
			return d, modfile.ModulePath(data)
		}
		if d == "." || d == "/" || d == "" {
			return "", ""
		}
	}
}
