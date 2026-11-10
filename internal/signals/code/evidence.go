package code

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/dev-danilocordeiro/sentinela/internal/signals"
)

// EvidenceTolerance é quantas linhas acima ou abaixo da informada o snippet pode começar. Modelos
// erram a linha por pouco com frequência; mais que isso já é outro trecho de código.
const EvidenceTolerance = 5

// InvalidEvidenceReason é o motivo gravado quando nenhuma evidência do achado se sustenta.
const InvalidEvidenceReason = "evidência inválida"

// EvidenceChecker confere cada evidência contra o checkout no SHA analisado: o arquivo existe,
// a linha está dentro dele e o snippet aparece perto da linha (ignorando espaços). Foi criado
// depois da EX-9, que citava a linha 1 com uma descrição em vez de código.
type EvidenceChecker struct {
	Dir   string
	lines map[string][]string // cache por arquivo; nil = não existe
}

// Check devolve as evidências válidas e um motivo para cada uma removida.
func (c *EvidenceChecker) Check(evidence []signals.Evidence) (kept []signals.Evidence, dropped []string) {
	for _, e := range evidence {
		if why := c.invalid(e); why != "" {
			dropped = append(dropped, fmt.Sprintf("%s:%d: %s", e.File, e.Line, why))
			continue
		}
		kept = append(kept, e)
	}
	return kept, dropped
}

func (c *EvidenceChecker) invalid(e signals.Evidence) string {
	lines, why := c.file(e.File)
	if why != "" {
		return why
	}
	if e.Line < 1 || e.Line > len(lines) {
		return fmt.Sprintf("linha fora do arquivo (%d linhas)", len(lines))
	}
	parts := snippetParts(e.Snippet)
	if len(parts) == 0 {
		return "snippet vazio"
	}
	from := max(0, e.Line-1-EvidenceTolerance)
	to := min(len(lines), e.Line+EvidenceTolerance+strings.Count(e.Snippet, "\n"))
	window := squash(strings.Join(lines[from:to], "\n"))
	for _, p := range parts {
		if !strings.Contains(window, p) {
			return fmt.Sprintf("snippet não aparece entre as linhas %d e %d", from+1, to)
		}
	}
	return ""
}

func (c *EvidenceChecker) file(name string) ([]string, string) {
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(name)))
	if name == "" || filepath.IsAbs(name) || clean == ".." || strings.HasPrefix(clean, "../") {
		return nil, "caminho fora do repositório"
	}
	if c.lines == nil {
		c.lines = map[string][]string{}
	}
	lines, seen := c.lines[clean]
	if !seen {
		data, err := os.ReadFile(filepath.Join(c.Dir, filepath.FromSlash(clean))) //nolint:gosec // caminho relativo validado acima
		if err == nil {
			lines = strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		}
		c.lines[clean] = lines
	}
	if lines == nil {
		return nil, "arquivo não existe no SHA analisado"
	}
	return lines, ""
}

// snippetParts separa o snippet nas reticências que os modelos usam para encurtar código; cada
// parte precisa aparecer no arquivo.
func snippetParts(s string) []string {
	s = strings.ReplaceAll(s, "…", "...")
	var out []string
	for _, p := range strings.Split(s, "...") {
		if q := squash(p); q != "" {
			out = append(out, q)
		}
	}
	return out
}

// squash remove todo espaço em branco, para comparar código sem depender de indentação.
func squash(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}
