package code

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/scan"
)

// osvSource monta um checkout com o lockfile do web e os sinais do osv-scanner da fixture.
func osvSource(t *testing.T) *Source {
	t.Helper()
	dir := t.TempDir()
	lock := "{\n  \"name\": \"web\",\n  \"lockfileVersion\": 3,\n  \"packages\": {\n" +
		"    \"node_modules/braces\": {\n      \"version\": \"3.0.3\",\n      \"dev\": true\n    },\n" +
		"    \"node_modules/next\": {\n      \"version\": \"15.5.25\"\n    },\n" +
		"    \"node_modules/postcss\": {\n      \"version\": \"8.4.31\"\n    },\n" +
		"    \"node_modules/sharp\": {\n      \"version\": \"0.35.4\",\n      \"optional\": true\n    },\n" +
		"    \"node_modules/source-map-js\": {\n      \"version\": \"1.2.1\"\n    }\n  }\n}\n"
	for rel, c := range map[string]string{"web/package-lock.json": lock, "web/package.json": "{\"name\":\"web\"}\n", "api/adapter/pom.xml": "<project/>\n"} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o750)
		if err := os.WriteFile(p, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := os.ReadFile(filepath.Join("..", "..", "scan", "testdata", "osv-npm-maven.json"))
	if err != nil {
		t.Fatal(err)
	}
	sigs, err := scan.OSVScanner{}.Parse(out, "/checkout")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join("..", "..", "..", "testdata", "sentinela.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return &Source{
		Cfg: cfg, Checkout: &repo.Checkout{Repo: "acme/go-servico-modular", Dir: dir, HeadSHA: "abc123"},
		Resolver: repo.NewResolver(cfg, repo.StaticCodeowners{}), ScanSignals: sigs,
	}
}

// Fase C, passo 4 (ADR 0058): um achado por lockfile ou manifesto. Entram as vulnerabilidades de
// produção com versão corrigida e severidade alta ou crítica; as outras (moderadas, só de
// desenvolvimento, sem correção) vão como resumo no corpo. A evidência é a linha da dependência
// no lockfile.
func TestOneFindingPerLockfileWithHighProductionVulnerabilities(t *testing.T) {
	s := osvSource(t)
	fs := s.VulnFindings(context.Background())
	if len(fs) != 1 {
		t.Fatalf("achados = %d, quer 1 (o pom.xml não tem vulnerabilidade casada)", len(fs))
	}
	f := fs[0]
	if f.Category != "security" || f.Severity != "high" || f.Module != "web" {
		t.Errorf("categoria/severidade/módulo = %s %s %s", f.Category, f.Severity, f.Module)
	}
	for _, w := range []string{"postcss para 8.5.", "sharp para 0.35.5", "source-map-js para 1.2.2", "em web"} {
		if !strings.Contains(f.Title, w) {
			t.Errorf("título sem %q: %s", w, f.Title)
		}
	}
	if strings.Contains(f.Title, "next") || strings.Contains(f.Title, "braces") {
		t.Errorf("moderada e de desenvolvimento não entram no título: %s", f.Title)
	}
	for _, w := range []string{"GHSA-wq5f-xc86-pv6w", "GHSA-68fv-2mgg-jv7q", "next 15.5.25", "GHSA-4jqv-mc3x-m676", "braces 3.0.3", "desenvolvimento"} {
		if !strings.Contains(f.Context, w) {
			t.Errorf("contexto sem %q:\n%s", w, f.Context)
		}
	}
	if e := f.Evidence[0]; e.File != "web/package-lock.json" || e.Line != 12 || e.Snippet != `"node_modules/postcss": {` {
		t.Errorf("evidência principal = %+v", e)
	}
	if !strings.Contains(f.ProposedFix, "npm") || len(f.AcceptanceCriteria) < 2 || f.Fingerprint == "" {
		t.Errorf("correção/critérios/fingerprint = %q %v %q", f.ProposedFix, f.AcceptanceCriteria, f.Fingerprint)
	}
	if again := s.VulnFindings(context.Background()); again[0].Fingerprint != f.Fingerprint {
		t.Error("fingerprint estável entre rodadas")
	}
}
