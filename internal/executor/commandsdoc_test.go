package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/signals"
)

// O AGENTS.md do workspace do achado é o principal no job.json, com o da raiz como reserva, e a
// spec diz de onde vêm os comandos.
func TestJobUsesTheCommandsDocNearestToTheFinding(t *testing.T) {
	e := setup(t, true)
	clone := filepath.Join(t.TempDir(), "c")
	gitIn(t, filepath.Dir(clone), "clone", "-q", e.remote, clone)
	write(clone, "cmd/api/AGENTS.md", "# API\n\n- teste: `go test ./cmd/api/...`\n")
	gitIn(t, clone, "add", ".")
	gitIn(t, clone, "commit", "-qm", "AGENTS.md do workspace")
	gitIn(t, clone, "push", "-q", "origin", "main")

	e.lin.AddLabel("EX-1", "sentinela-fix")
	_, _ = e.w.Enqueue(context.Background())
	if job := e.runOnce(t); job.Status != "succeeded" {
		t.Fatalf("job = %s: %s", job.Status, job.Error.String)
	}
	inv := e.runner.invocations()
	if len(inv) == 0 {
		t.Fatal("o job não rodou")
	}
	data, err := os.ReadFile(filepath.Join(inv[0].JobDir, "job.json"))
	if err != nil {
		t.Fatal(err)
	}
	var spec JobSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.CommandsDoc != "cmd/api/AGENTS.md" || !slices.Equal(spec.CommandsDocs, []string{"cmd/api/AGENTS.md", "AGENTS.md"}) {
		t.Errorf("commands_doc = %q, commands_docs = %q", spec.CommandsDoc, spec.CommandsDocs)
	}
	md, err := os.ReadFile(filepath.Join(inv[0].JobDir, "spec.md"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "são os de `cmd/api/AGENTS.md`, o mais próximo do escopo; o setup e o que ele não cobrir estão em `AGENTS.md`"; !strings.Contains(string(md), want) {
		t.Errorf("spec sem %q:\n%s", want, md)
	}
}

func TestCommandsDocFailsOnlyWithoutAnyDocForTheScope(t *testing.T) {
	wt := t.TempDir()
	write(wt, "apps/bff/AGENTS.md", "x")
	f := signals.Finding{ScopePaths: []string{"apps/bff/src"}, Evidence: []signals.Evidence{{File: "apps/bff/src/a.ts"}}}
	if doc, docs, err := CommandsDoc(wt, "acme/mono", f.CommandsScope()); err != nil || doc != "apps/bff/AGENTS.md" || len(docs) != 1 {
		t.Errorf("BFF = %q %q %v", doc, docs, err)
	}
	web := signals.Finding{Evidence: []signals.Evidence{{File: "apps/web/src/a.ts"}}}
	_, _, err := CommandsDoc(wt, "acme/mono", web.CommandsScope())
	if err == nil || !strings.Contains(err.Error(), "não tem AGENTS.md nem CLAUDE.md no escopo do achado nem na raiz") {
		t.Errorf("web sem arquivo = %v", err)
	}
	if got := SpecFromFinding(JobSpec{CommandsDoc: "AGENTS.md", CommandsDocs: []string{"AGENTS.md"}}, signals.Finding{}); !strings.Contains(got, "- Os comandos de build, teste e lint são os de `AGENTS.md`.\n") {
		t.Errorf("spec com um arquivo só:\n%s", got)
	}
}

// Num monorepo de front e BFF, o job.json diz o workspace afetado (diretório, gerenciador e pacote)
// e a spec manda rodar os testes dele.
func TestJobSpecNamesTheAffectedWorkspace(t *testing.T) {
	fixture := filepath.Join("..", "..", "testdata", "ts-monorepo")
	f := signals.Finding{ScopePaths: []string{"apps/bff/src/routes"}, Evidence: []signals.Evidence{{File: "apps/bff/src/routes/orders.ts", Line: 17}},
		AcceptanceCriteria: []string{"cancel espera o backend"}}
	ws := WorkspacesFor(fixture, f.CommandsScope())
	if len(ws) != 1 || ws[0] != (Workspace{Dir: "apps/bff", Kind: "pnpm", Name: "@acme/bff"}) {
		t.Fatalf("workspaces = %+v", ws)
	}
	got := SpecFromFinding(JobSpec{CommandsDoc: "apps/bff/AGENTS.md", CommandsDocs: []string{"apps/bff/AGENTS.md", "AGENTS.md"}, Workspaces: ws}, f)
	if want := "- Workspace afetado: `apps/bff` (pnpm, pacote `@acme/bff`): rode os testes dele, com o filtro do gerenciador.\n"; !strings.Contains(got, want) {
		t.Errorf("spec sem %q:\n%s", want, got)
	}
}
