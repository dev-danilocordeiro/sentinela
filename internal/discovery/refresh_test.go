package discovery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pgvector/pgvector-go"

	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/scan"
	"github.com/dev-danilocordeiro/sentinela/internal/signals/code"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// fakeGovulncheck põe no PATH um govulncheck que imprime uma vulnerabilidade da stdlib chamada
// de cmd/main.go:5 (corrigida em v1.23.4).
func fakeGovulncheck(t *testing.T, checkout string) {
	t.Helper()
	bin := t.TempDir()
	main := filepath.Join(checkout, "cmd", "main.go")
	stream := `{"SBOM":{"roots":["acme.com/svc/cmd"]}}
{"osv":{"id":"GO-1","summary":"Panic no parser HTTP/2"}}
{"finding":{"osv":"GO-1","fixed_version":"v1.23.4","trace":[{"module":"stdlib","version":"v1.23.0","package":"net/http","function":"Get","position":{"filename":"src/net/http/client.go","line":3}},{"module":"acme.com/svc","package":"acme.com/svc/cmd","function":"main","position":{"filename":"` + main + `","line":5}}]}}
`
	if err := os.WriteFile(filepath.Join(bin, "stream.json"), []byte(stream), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat " + filepath.Join(bin, "stream.json") + "\n"
	if err := os.WriteFile(filepath.Join(bin, "govulncheck"), []byte(script), 0o700); err != nil { //nolint:gosec // script de teste
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// O achado 31 real: gravado antes do ajuste de texto ("módulo ."). A regeneração reescreve o
// conteúdo e mantém estado, roteamento, severidade, veredito do Judge e fingerprint.
func TestRefreshVulnFindingRewritesTextAndKeepsDecisions(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	co, err := e.runner.Git.Sync(ctx, "acme/svc", "main")
	if err != nil {
		t.Fatal(err)
	}
	fakeGovulncheck(t, co.Dir)
	sigs, _ := (&scan.Runner{Scanners: []scan.Scanner{scan.Govulncheck{}}}).Run(ctx, co.Dir, map[string]bool{"go": true})
	src := &code.Source{Cfg: e.runner.Cfg, Checkout: co, Resolver: repo.NewResolver(e.runner.Cfg, repo.StaticCodeowners{}), ScanSignals: sigs}
	want := src.VulnFindings(ctx)
	if len(want) != 1 {
		t.Fatalf("achados do govulncheck falso = %d", len(want))
	}
	run, err := e.st.Q.CreateRun(ctx, db.CreateRunParams{Repo: "acme/svc", HeadSha: text(co.HeadSHA), BudgetUsd: store.NanoUSDToNumeric(0), Mode: "full"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := e.st.Q.InsertFinding(ctx, db.InsertFindingParams{
		RunID: pgtype.Int8{Int64: run.ID, Valid: true}, Fingerprint: want[0].Fingerprint, Source: "code_analysis", Repo: "acme/svc",
		Module: ".", Owners: []string{}, Routing: []byte(`{"team":"core","rule":"repo"}`), Category: "security", Severity: "high",
		Confidence: 0.9, Title: "Atualiza Go para 1.23.4 por 1 vulnerabilidade(s) chamada(s)", Context: "texto antigo do módulo .",
		Evidence: []byte("[]"), Signals: []byte("[]"), AcceptanceCriteria: []string{}, ScopePaths: []string{}, RelatedModules: []string{},
		Embedding: pgvector.NewVector(make([]float32, 1024)), JudgeConfidence: pgtype.Float4{Float32: 0.8, Valid: true},
		JudgeReason: pgtype.Text{String: "chamadas reais", Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := e.runner.RefreshVulnFinding(ctx, id); err != nil {
		t.Fatal(err)
	}
	got, err := e.st.Q.GetFinding(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Context != want[0].Context || got.Context == "texto antigo do módulo ." || got.Module != want[0].Module || !strings.Contains(string(got.Evidence), "cmd/main.go") {
		t.Errorf("texto não regenerado: módulo %q, contexto %q, evidência %s", got.Module, got.Context, got.Evidence)
	}
	if got.State != "open" || got.Severity != "high" || got.JudgeConfidence.Float32 != 0.8 || got.JudgeReason.String != "chamadas reais" ||
		got.Fingerprint != want[0].Fingerprint || !strings.Contains(string(got.Routing), `"rule": "repo"`) {
		t.Errorf("decisão alterada: %+v", got)
	}

	// Com issue publicada, não mexe mais.
	e.publish(t, id, "EX-1")
	if _, _, err := e.runner.RefreshVulnFinding(ctx, id); err == nil || !strings.Contains(err.Error(), "nada gravado") {
		t.Errorf("achado publicado regenerado: %v", err)
	}
}
