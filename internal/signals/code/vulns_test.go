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

// vulnStream é a saída do govulncheck -format json num módulo com:
//   - GO-1 na stdlib, chamada de cmd/api/main.go:5 (corrigida em v1.27.2);
//   - GO-2 na stdlib, chamada (corrigida em v1.27.1: o alvo continua 1.27.2);
//   - GO-3 em golang.org/x/net, só exigida (v0.60.0): fica no resumo, sem ação;
//   - GO-4 em golang.org/x/crypto, só importada.
func vulnStream(dir string) string {
	main := filepath.Join(dir, "cmd", "api", "main.go")
	return `{"config":{"go_version":"go1.27.0"}}
{"osv":{"id":"GO-1","summary":"Panic no parser HTTP/2"}}
{"osv":{"id":"GO-2","summary":"Consumo de memória no net/http"}}
{"osv":{"id":"GO-3","summary":"Bug no x/net"}}
{"osv":{"id":"GO-4","summary":"Bug no x/crypto"}}
{"finding":{"osv":"GO-1","fixed_version":"v1.27.2","trace":[{"module":"stdlib","version":"v1.27.0"}]}}
{"finding":{"osv":"GO-1","fixed_version":"v1.27.2","trace":[{"module":"stdlib","version":"v1.27.0","package":"net/http"}]}}
{"finding":{"osv":"GO-1","fixed_version":"v1.27.2","trace":[{"module":"stdlib","version":"v1.27.0","package":"net/http","function":"ListenAndServe","receiver":"*Server","position":{"filename":"src/net/http/server.go","line":10}},{"module":"acme.com/svc","package":"acme.com/svc/cmd/api","function":"main","position":{"filename":"` + main + `","line":5}}]}}
{"finding":{"osv":"GO-2","fixed_version":"v1.27.1","trace":[{"module":"stdlib","version":"v1.27.0","package":"net/http","function":"Get","position":{"filename":"src/net/http/client.go","line":3}},{"module":"acme.com/svc","package":"acme.com/svc/cmd/api","function":"main","position":{"filename":"` + main + `","line":6}}]}}
{"finding":{"osv":"GO-3","fixed_version":"v0.60.0","trace":[{"module":"golang.org/x/net","version":"v0.58.0"}]}}
{"finding":{"osv":"GO-4","fixed_version":"v0.56.0","trace":[{"module":"golang.org/x/crypto","version":"v0.55.0","package":"golang.org/x/crypto/ssh"}]}}
`
}

func vulnSource(t *testing.T, stream func(dir string) string) *Source {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":          "module acme.com/svc\n\ngo 1.27.0\n\nrequire (\n\tgolang.org/x/crypto v0.55.0\n\tgolang.org/x/net v0.58.0\n)\n",
		"cmd/api/main.go": "package main\n\nimport \"net/http\"\n\nfunc main() { _ = http.ListenAndServe(\":8080\", nil)\n\t_, _ = http.Get(\"http://x\") }\n",
	}
	for rel, c := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o750)
		if err := os.WriteFile(p, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sigs, _, err := scan.Govulncheck{}.ParseFiltered([]byte(stream(dir)), dir)
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

func TestOneFindingPerGoModWithOnlyCalledVulnerabilities(t *testing.T) {
	s := vulnSource(t, vulnStream)
	for _, sg := range s.ScanSignals {
		if sg.Vuln == nil || sg.Vuln.Manifest != "go.mod" {
			t.Fatalf("sinal sem detalhe de vulnerabilidade: %+v", sg)
		}
	}
	fs := s.VulnFindings(context.Background())
	if len(fs) != 1 {
		t.Fatalf("achados = %d, quer 1", len(fs))
	}
	f := fs[0]
	if f.Title != "Atualiza Go para 1.27.2 por 2 vulnerabilidade(s) chamada(s)" || f.Category != "security" || f.Severity != "high" {
		t.Errorf("título/categoria/severidade = %q %s %s", f.Title, f.Category, f.Severity)
	}
	if e := f.Evidence[0]; e.File != "go.mod" || e.Line != 3 || e.Snippet != "go 1.27.0" {
		t.Errorf("evidência principal = %+v", e)
	}
	if len(f.Evidence) != 3 || f.Evidence[1].File != "cmd/api/main.go" || f.Evidence[1].Line != 5 || f.Evidence[2].Line != 6 {
		t.Errorf("chamadas como evidência = %+v", f.Evidence)
	}
	for _, want := range []string{"GO-1 (stdlib v1.27.0, corrigido em v1.27.2)", "net/http.Server.ListenAndServe, chamado de cmd/api/main.go:5", "GO-2", "Só importadas ou exigidas", "golang.org/x/crypto v0.55.0 (corrigidas até v0.56.0): GO-4", "golang.org/x/net v0.58.0 (corrigidas até v0.60.0): GO-3", "do módulo raiz"} {
		if !strings.Contains(f.Context, want) {
			t.Errorf("contexto sem %q:\n%s", want, f.Context)
		}
	}
	if strings.Contains(f.ProposedFix, "x/net") || !strings.Contains(f.ProposedFix, "de 1.27.0 para 1.27.2") {
		t.Errorf("ação sugerida = %s (só importadas não geram ação)", f.ProposedFix)
	}
	if strings.Join(f.ScopePaths, ",") != "go.mod,go.sum" || f.Routing.Team != "time-exemplo" || f.Module != "." {
		t.Errorf("escopo %v, time %s, módulo %s", f.ScopePaths, f.Routing.Team, f.Module)
	}
	if len(f.AcceptanceCriteria) < 2 || !strings.Contains(f.AcceptanceCriteria[0], "GO-1, GO-2") || !strings.Contains(f.AcceptanceCriteria[1], "`go 1.27.2` ou maior") {
		t.Errorf("critérios = %v", f.AcceptanceCriteria)
	}
	if len(f.Fingerprint) != 12 || s.ProblemKeys[f.Fingerprint] != "govulncheck stdlib@v1.27.2" {
		t.Errorf("fingerprint %q, chave %q", f.Fingerprint, s.ProblemKeys[f.Fingerprint])
	}
	ev := &EvidenceChecker{Dir: s.Checkout.Dir}
	for _, e := range f.Evidence {
		if why := ev.invalid(e); why != "" {
			t.Errorf("evidência %s:%d inválida: %s", e.File, e.Line, why)
		}
	}
}

// Outra versão alvo muda o fingerprint (o comum descarta números).
func TestVulnFingerprintChangesWithTheTargetVersion(t *testing.T) {
	a := vulnSource(t, vulnStream).VulnFindings(context.Background())[0]
	b := vulnSource(t, func(dir string) string {
		return strings.ReplaceAll(vulnStream(dir), `"fixed_version":"v1.27.2"`, `"fixed_version":"v1.27.3"`)
	}).VulnFindings(context.Background())[0]
	if a.Fingerprint == b.Fingerprint || !strings.Contains(b.Title, "1.27.3") {
		t.Errorf("fingerprints %s e %s, título %q", a.Fingerprint, b.Fingerprint, b.Title)
	}
}

// Só importadas ou exigidas: nenhum achado, e os sinais não vão para a triagem do pacote.
func TestNoCalledVulnerabilityNoFinding(t *testing.T) {
	s := vulnSource(t, func(dir string) string {
		var keep []string
		for _, l := range strings.Split(vulnStream(dir), "\n") {
			if !strings.Contains(l, `"function"`) {
				keep = append(keep, l)
			}
		}
		return strings.Join(keep, "\n")
	})
	if fs := s.VulnFindings(context.Background()); len(fs) != 0 {
		t.Fatalf("achados = %+v", fs)
	}
	in, err := s.triageInput(repo.Package{Path: "."}, repo.Resolution{})
	if err != nil {
		t.Fatal(err)
	}
	if len(in.ScannerSignals) != 0 {
		t.Errorf("sinais do govulncheck na triagem: %+v", in.ScannerSignals)
	}
}
