// Package repo trata do conhecimento estático sobre repositórios: CODEOWNERS
// e resolução de ownership. Clone, diff e grafo de dependências entram na Fase 2.
package repo

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// CodeownersPaths são os locais onde o GitHub procura o arquivo, em ordem de precedência.
// Fonte: https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/about-code-owners#codeowners-file-location
var CodeownersPaths = []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"}

// CodeownersRule é uma linha do CODEOWNERS.
type CodeownersRule struct {
	Line    int      // linha no arquivo (1-based), usada em routing.rule = "codeowners:L<n>"
	Pattern string   // padrão como escrito no arquivo
	Owners  []string // na ordem do arquivo; o primeiro é o dono principal
	globs   []string
}

// Codeowners é um arquivo CODEOWNERS já interpretado.
type Codeowners struct {
	Rules []CodeownersRule
}

// ParseCodeowners interpreta o arquivo com a sintaxe do GitHub: comentários com #,
// padrão seguido de zero ou mais donos. Uma regra sem donos é válida e remove o dono
// de caminhos que ela casa.
func ParseCodeowners(r io.Reader) (*Codeowners, error) {
	co := &Codeowners{}
	sc := bufio.NewScanner(r)
	line := 0
	for sc.Scan() {
		line++
		text := sc.Text()
		if i := strings.Index(text, " #"); i >= 0 {
			text = text[:i]
		}
		text = strings.TrimSpace(text)
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Fields(text)
		rule := CodeownersRule{Line: line, Pattern: fields[0], Owners: fields[1:]}
		globs, err := compileCodeownersPattern(rule.Pattern)
		if err != nil {
			return nil, fmt.Errorf("CODEOWNERS linha %d: %w", line, err)
		}
		rule.globs = globs
		co.Rules = append(co.Rules, rule)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("lendo CODEOWNERS: %w", err)
	}
	return co, nil
}

// Match devolve a última regra que casa com o caminho (semântica do GitHub).
func (co *Codeowners) Match(path string) (CodeownersRule, bool) {
	if co == nil {
		return CodeownersRule{}, false
	}
	path = strings.TrimPrefix(path, "/")
	for i := len(co.Rules) - 1; i >= 0; i-- {
		if co.Rules[i].matches(path) {
			return co.Rules[i], true
		}
	}
	return CodeownersRule{}, false
}

// Owners devolve todos os donos distintos citados no arquivo, na ordem em que aparecem.
func (co *Codeowners) Owners() []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range co.Rules {
		for _, o := range r.Owners {
			if !seen[o] {
				seen[o] = true
				out = append(out, o)
			}
		}
	}
	return out
}

func (r CodeownersRule) matches(path string) bool {
	for _, g := range r.globs {
		if ok, _ := doublestar.Match(g, path); ok {
			return true
		}
	}
	return false
}

// compileCodeownersPattern traduz um padrão no estilo gitignore para globs doublestar:
//   - "/" no início, ou uma barra no meio, ancoram o padrão na raiz;
//     sem isso ele casa em qualquer profundidade;
//   - "/" no fim restringe a diretório: casa tudo abaixo dele;
//   - um padrão cujo último segmento não tem curinga também casa como diretório
//     ("**/logs" casa "a/logs/x"), mas "docs/*" não casa "docs/a/b", como no GitHub.
func compileCodeownersPattern(p string) ([]string, error) {
	dirOnly := strings.HasSuffix(p, "/")
	body := strings.Trim(p, "/")
	if body == "" {
		return nil, fmt.Errorf("padrão vazio %q", p)
	}
	anchored := strings.HasPrefix(p, "/") || strings.Contains(body, "/")
	if !anchored {
		body = "**/" + body
	}
	if !doublestar.ValidatePattern(body) {
		return nil, fmt.Errorf("padrão inválido %q", p)
	}
	last := body[strings.LastIndex(body, "/")+1:]
	switch {
	case dirOnly:
		return []string{body + "/**"}, nil
	case !strings.ContainsAny(last, "*?["):
		return []string{body, body + "/**"}, nil
	default:
		return []string{body}, nil
	}
}
