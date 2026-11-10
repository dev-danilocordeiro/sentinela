package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
)

// docFiles são os documentos de uso: README, docs/*.md e as fontes do guia (sem ADRs e relatórios, que são históricos),
// CONTRIBUTING e o exemplo.
func docFiles(t *testing.T) []string {
	t.Helper()
	root := filepath.Join("..", "..")
	files := []string{filepath.Join(root, "README.md"), filepath.Join(root, "CONTRIBUTING.md"), filepath.Join(root, "examples", "monorepo-acme", "README.md")}
	more, _ := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	files = append(files, more...)
	guideSources, _ := filepath.Glob(filepath.Join(root, "docs", "guia", "fontes", "*.md"))
	files = append(files, guideSources...)
	// A skill /sentinela-setup roda os comandos que cita: eles precisam existir (docs e skill andam juntos).
	skill, _ := filepath.Glob(filepath.Join(root, ".claude", "skills", "*", "*.md"))
	files = append(files, skill...)
	var out []string
	for _, f := range files {
		if _, err := os.Stat(f); err == nil {
			out = append(out, f)
		}
	}
	return out
}

var (
	codeSpan = regexp.MustCompile("`([^`\n]+)`")
	fence    = regexp.MustCompile("(?s)```[a-z]*\n(.*?)```")
	cmdRe    = regexp.MustCompile(`(?:^|[\s(])(?:\./)?sentinela((?:\s+--config\s+\S+)?(?:\s+[a-z][a-z-]*)+)((?:\s+(?:--[a-z][a-z-]*|-[a-z]|[^\s|;&>#]+))*)`)
	// pgFlag: "-U sentinela" e afins são o usuário ou o banco do Postgres, não o binário.
	pgFlag = regexp.MustCompile(`-[Ud]\s+sentinela\b`)
	keyRe  = regexp.MustCompile(`^[a-z_]+(?:\.(?:[a-z_]+|<[a-z]+>|\[\]|\*))+$`)
)

// commandAt acha o comando cobra pelos nomes (para nos argumentos posicionais).
func commandAt(root *cobra.Command, words []string) (*cobra.Command, []string) {
	cmd := root
	for i, w := range words {
		var next *cobra.Command
		for _, c := range cmd.Commands() {
			if c.Name() == w {
				next = c
			}
		}
		if next == nil {
			return cmd, words[i:]
		}
		cmd = next
	}
	return cmd, nil
}

// Todo comando e flag citados nos documentos existe no binário.
func TestDocsCommandsAndFlagsExist(t *testing.T) {
	root := NewRootCmd(func(string) string { return "" })
	var bad []string
	for _, f := range docFiles(t) {
		data, _ := os.ReadFile(f)
		var snippets []string
		for _, m := range fence.FindAllStringSubmatch(string(data), -1) {
			snippets = append(snippets, strings.Split(m[1], "\n")...)
		}
		for _, m := range codeSpan.FindAllStringSubmatch(string(data), -1) {
			snippets = append(snippets, m[1])
		}
		for _, s := range snippets {
			s = pgFlag.ReplaceAllString(s, "")
			for _, m := range cmdRe.FindAllStringSubmatch(s, -1) {
				words := strings.Fields(regexp.MustCompile(`--config\s+\S+`).ReplaceAllString(m[1], ""))
				cmd, rest := commandAt(root, words)
				if cmd == root || (len(rest) > 0 && cmd.HasSubCommands()) {
					bad = append(bad, filepath.Base(f)+": comando inexistente: sentinela"+m[1])
					continue
				}
				for _, tok := range strings.Fields(m[2]) {
					if !strings.HasPrefix(tok, "--") {
						continue
					}
					name, _, _ := strings.Cut(strings.TrimPrefix(tok, "--"), "=")
					if cmd.Flags().Lookup(name) == nil && cmd.InheritedFlags().Lookup(name) == nil && root.PersistentFlags().Lookup(name) == nil {
						bad = append(bad, filepath.Base(f)+": flag inexistente: sentinela "+strings.Join(words, " ")+" --"+name)
					}
				}
			}
		}
	}
	reportDocs(t, bad)
}

// schemaKeys junta os caminhos de chave do schema (sentinela.yaml e arquivo de time).
func schemaKeys(t *testing.T) map[string]bool {
	t.Helper()
	keys := map[string]bool{}
	var walk func(prefix string, s map[string]any)
	walk = func(prefix string, s map[string]any) {
		if prefix != "" {
			keys[prefix] = true
		}
		if props, ok := s["properties"].(map[string]any); ok {
			for k, v := range props {
				walk(strings.TrimPrefix(prefix+"."+k, "."), v.(map[string]any))
			}
		}
		if items, ok := s["items"].(map[string]any); ok {
			walk(prefix+".[]", items)
		}
		if ap, ok := s["additionalProperties"].(map[string]any); ok {
			walk(prefix+".*", ap)
		}
	}
	for _, gen := range []func() ([]byte, error){config.Schema, config.TeamSchema} {
		b, err := gen()
		if err != nil {
			t.Fatal(err)
		}
		var s map[string]any
		if err := json.Unmarshal(b, &s); err != nil {
			t.Fatal(err)
		}
		walk("", s)
	}
	return keys
}

// normalizeKey troca placeholders (<time>, <nome>, [] e *) pela forma do schema.
func normalizeKey(k string) string {
	parts := strings.Split(k, ".")
	for i, p := range parts {
		switch {
		case strings.HasPrefix(p, "<") || p == "*":
			parts[i] = "*"
		case p != "[]" && strings.HasSuffix(p, "[]"):
			parts[i] = strings.TrimSuffix(p, "[]") + ".[]"
		}
	}
	return strings.Join(parts, ".")
}

// Toda chave de config citada nos documentos (`a.b.c`) existe no schema gerado do código.
func TestDocsConfigKeysExist(t *testing.T) {
	keys := schemaKeys(t)
	tops := map[string]bool{}
	for k := range keys {
		top, _, _ := strings.Cut(k, ".")
		tops[top] = true
	}
	var bad []string
	for _, f := range docFiles(t) {
		data, _ := os.ReadFile(f)
		for _, m := range codeSpan.FindAllStringSubmatch(string(data), -1) {
			k := strings.TrimSpace(m[1])
			k = strings.ReplaceAll(k, "repos[]", "repos.[]")
			k = strings.ReplaceAll(k, "modules[]", "modules.[]")
			if !keyRe.MatchString(k) {
				continue
			}
			top, _, _ := strings.Cut(k, ".")
			if !tops[top] {
				continue // outro tipo de identificador (pacote Go, tabela, campo de JSON)
			}
			if n := normalizeKey(k); !keyExists(keys, n) {
				bad = append(bad, filepath.Base(f)+": chave inexistente: "+k)
			}
		}
	}
	reportDocs(t, bad)
}

func reportDocs(t *testing.T, bad []string) {
	t.Helper()
	sort.Strings(bad)
	seen := map[string]bool{}
	for _, b := range bad {
		if !seen[b] {
			seen[b] = true
			t.Error(b)
		}
	}
}

// keyExists aceita a chave inteira, um grupo ("labels.*": alguma chave abaixo de labels) ou uma
// chave relativa a um bloco ("policy.auto_approve" dentro de defaults ou teams.<time>).
func keyExists(keys map[string]bool, k string) bool {
	if keys[k] {
		return true
	}
	// Nome concreto no lugar de um mapa (teams.pagamentos.x = teams.<time>.x).
	parts := strings.Split(k, ".")
	for i := 1; i < len(parts); i++ {
		alt := append(append(append([]string{}, parts[:i]...), "*"), parts[i+1:]...)
		if keys[strings.Join(alt, ".")] {
			return true
		}
	}
	if g, ok := strings.CutSuffix(k, ".*"); ok {
		for key := range keys {
			if strings.HasPrefix(key, g+".") {
				return true
			}
		}
	}
	for key := range keys {
		if strings.HasSuffix(key, "."+k) {
			return true
		}
	}
	return false
}

// O próprio verificador pega chave e comando errados (para não passar por engano).
func TestDocsCheckerCatchesMistakes(t *testing.T) {
	keys := schemaKeys(t)
	for _, k := range []string{"adapters.trackers", "teams.<time>.labels.gatilho", "discovery.max_new_repo_per_run"} {
		if keyExists(keys, normalizeKey(k)) {
			t.Errorf("chave errada aceita: %s", k)
		}
	}
	for _, k := range []string{"adapters.tracker", "teams.<time>.labels.trigger", "repos[].modules[].linear_project", "teams.<time>.linear.category_projects", "execution.failed_worktree_retention"} {
		if !keyExists(keys, normalizeKey(strings.NewReplacer("repos[]", "repos.[]", "modules[]", "modules.[]").Replace(k))) {
			t.Errorf("chave certa recusada: %s", k)
		}
	}
	root := NewRootCmd(func(string) string { return "" })
	if cmd, rest := commandAt(root, []string{"config", "validat"}); len(rest) == 0 || !cmd.HasSubCommands() {
		t.Error("comando errado aceito: config validat")
	}
	if cmd, _ := commandAt(root, []string{"config", "validate"}); cmd.Flags().Lookup("changed") == nil || cmd.Flags().Lookup("mudados") != nil {
		t.Error("flags do validate")
	}
}
