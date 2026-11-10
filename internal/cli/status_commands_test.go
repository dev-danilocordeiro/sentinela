package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
)

// O status lista, pelos clones do scan, os repos sem AGENTS.md nem CLAUDE.md, os que só têm nos
// workspaces e os ainda sem clone.
func TestStatusListsReposWithoutCommandsDoc(t *testing.T) {
	work := t.TempDir()
	for name, file := range map[string]string{"acme/com-agents": "AGENTS.md", "acme/sem-arquivo": "README.md", "acme/workspaces": "apps/bff/AGENTS.md"} {
		dir := filepath.Join(work, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, file)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{Discovery: config.Discovery{Workdir: work},
		Repos: []*config.Repo{{Name: "acme/com-agents"}, {Name: "acme/sem-arquivo"}, {Name: "acme/novo"}, {Name: "acme/workspaces"}}}
	var out bytes.Buffer
	printCommandsDocs(&out, cfg)
	for _, want := range []string{
		"sem AGENTS.md nem CLAUDE.md (a correção não roda; clone do último scan): acme/sem-arquivo\n",
		"ainda sem clone (confira com config validate): acme/novo\n",
		"sem AGENTS.md nem CLAUDE.md na raiz (a correção só roda nos workspaces com o arquivo): acme/workspaces (só apps/bff/AGENTS.md)\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status sem %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "com-agents") {
		t.Errorf("repo com AGENTS.md listado:\n%s", out.String())
	}
}
