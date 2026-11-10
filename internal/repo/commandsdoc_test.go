package repo

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestLocalCommandsDoc(t *testing.T) {
	work := t.TempDir()
	mk := func(name string, files ...string) {
		dir := filepath.Join(work, "o", name)
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("ambos", "CLAUDE.md", "AGENTS.md")
	mk("claude", "CLAUDE.md")
	mk("nenhum", "README.md")
	if err := os.MkdirAll(filepath.Join(work, "o", "diretorio", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(work, "o", "diretorio", "AGENTS.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]struct {
		doc    string
		cloned bool
	}{
		"o/ambos": {"AGENTS.md", true}, "o/claude": {"CLAUDE.md", true}, "o/nenhum": {"", true},
		"o/diretorio": {"", true}, "o/sem-clone": {"", false},
	} {
		doc, cloned := LocalCommandsDoc(work, name)
		if doc != want.doc || cloned != want.cloned {
			t.Errorf("%s: %q %v, quer %q %v", name, doc, cloned, want.doc, want.cloned)
		}
	}
}

func TestCommandsDocsForPicksTheNearestToTheScopeWithTheRootAsFallback(t *testing.T) {
	fixture := filepath.Join("..", "..", "testdata", "ts-monorepo")
	tmp := t.TempDir()
	writeTree(t, tmp, map[string]string{
		"services/a/CLAUDE.md":           "x",
		"services/a/AGENTS.md":           "x",
		"services/b/CLAUDE.md":           "x",
		"services/b/src/handler.ts":      "x",
		"services/c/src/x.ts":            "x",
		"packages/sem-raiz/AGENTS.md":    "x",
		"packages/sem-raiz/src/index.ts": "x",
	})
	for _, c := range []struct {
		name  string
		dir   string
		scope []string
		want  []string
	}{
		{"BFF: o do workspace e o da raiz", fixture, []string{"apps/bff/src/routes/orders.ts"}, []string{"apps/bff/AGENTS.md", "AGENTS.md"}},
		{"BFF por diretório do escopo", fixture, []string{"apps/bff/src/"}, []string{"apps/bff/AGENTS.md", "AGENTS.md"}},
		{"glob no escopo", fixture, []string{"apps/bff/**"}, []string{"apps/bff/AGENTS.md", "AGENTS.md"}},
		{"web sem AGENTS.md próprio: o da raiz", fixture, []string{"apps/web/src/lib/api.ts"}, []string{"AGENTS.md"}},
		{"web e BFF: na ordem do escopo, sem repetir a raiz", fixture, []string{"apps/web/src/lib", "apps/bff/src/backend/orders.ts"}, []string{"AGENTS.md", "apps/bff/AGENTS.md"}},
		{"sem escopo: só a raiz", fixture, nil, []string{"AGENTS.md"}},
		{"caminho que sai do repo é ignorado", fixture, []string{"../fora/x.ts"}, []string{"AGENTS.md"}},
		{"AGENTS.md vence CLAUDE.md no mesmo diretório", tmp, []string{"services/a/x.ts"}, []string{"services/a/AGENTS.md"}},
		{"CLAUDE.md do workspace serve", tmp, []string{"services/b/src/handler.ts"}, []string{"services/b/CLAUDE.md"}},
		{"sem nenhum no caminho nem na raiz", tmp, []string{"services/c/src/x.ts"}, nil},
		{"só no workspace, sem raiz", tmp, []string{"packages/sem-raiz/src/index.ts"}, []string{"packages/sem-raiz/AGENTS.md"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := CommandsDocsFor(c.dir, c.scope); !slices.Equal(got, c.want) {
				t.Errorf("CommandsDocsFor(%q) = %q, quer %q", c.scope, got, c.want)
			}
		})
	}
	if got := WorkspaceCommandsDocs(tmp); !slices.Equal(got, []string{"packages/sem-raiz/AGENTS.md", "services/a/AGENTS.md", "services/b/CLAUDE.md"}) {
		t.Errorf("WorkspaceCommandsDocs = %q", got)
	}
}
