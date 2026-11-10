package scan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// As fixtures são saídas reais das ferramentas sobre um módulo de teste com
// main.go (os.Open sem checar erro, md5, chmod 0777) e um teste que falha.
func TestParsersNormalizeRealToolOutput(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module probe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		scanner Scanner
		file    string
		want    Signal // primeiro sinal esperado
		min     int
	}{
		{"golangci-lint", GolangciLint{}, "golangci-lint.json",
			Signal{Scanner: "golangci-lint", Rule: "errcheck", File: "main.go", Line: 12}, 1},
		{"gosec", Gosec{}, "gosec.json",
			Signal{Scanner: "gosec", Rule: "G703", File: "main.go", Line: 10, Severity: "high"}, 3},
		{"govulncheck", Govulncheck{}, "govulncheck.jsonl",
			Signal{Scanner: "govulncheck", Rule: "GO-2026-6599", File: "go.mod", Line: 1, Severity: "high"}, 1},
		{"semgrep", Semgrep{}, "semgrep.json",
			Signal{Scanner: "semgrep", Rule: "go.lang.security.audit.crypto.use_of_weak_crypto.use-of-md5", File: "main.go", Line: 11, Severity: "warning"}, 1},
		{"go test", GoTest{}, "gotest.jsonl",
			Signal{Scanner: "go-test", Rule: "TestFails", File: ".", Package: ".", Severity: "high"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sigs, err := tc.scanner.Parse(fixture(t, tc.file), dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(sigs) < tc.min {
				t.Fatalf("sinais = %d, quer ao menos %d", len(sigs), tc.min)
			}
			got := sigs[0]
			if got.Scanner != tc.want.Scanner || got.Rule != tc.want.Rule || got.File != tc.want.File || got.Line != tc.want.Line ||
				(tc.want.Severity != "" && got.Severity != tc.want.Severity) || got.Package != tc.want.Package {
				t.Errorf("primeiro sinal = %+v, quer %+v", got, tc.want)
			}
			if got.Message == "" {
				t.Error("mensagem vazia")
			}
		})
	}
}

func TestRunnerSkipsUnavailableAndNotApplicableScanners(t *testing.T) {
	r := &Runner{
		Scanners: []Scanner{GolangciLint{}, Semgrep{}},
		LookPath: func(bin string) (string, error) { return "", errors.New("não achou " + bin) },
	}
	sigs, results := r.Run(context.Background(), t.TempDir(), map[string]bool{"java": true})
	if len(sigs) != 0 {
		t.Fatalf("sinais = %v", sigs)
	}
	if results[0].Status != StatusSkipped || results[0].Detail != "não se aplica às linguagens do repo" ||
		results[1].Status != StatusSkipped || results[1].Detail != "semgrep não está no PATH" {
		t.Errorf("results = %+v", results)
	}
}

// Ferramenta que termina sem erro mas não analisou nada não é "ok": vira failed com a saída.
func TestScannerThatAnalyzedNothingFails(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(t.TempDir(), "semgrep")
	script := "#!/bin/sh\n" + `echo '{"results":[],"errors":[],"paths":{"scanned":[]}}'` + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { //nolint:gosec // script de teste executável
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, results := (&Runner{Scanners: []Scanner{Semgrep{}}}).Run(context.Background(), dir, map[string]bool{})
	if r := results[0]; r.Status != StatusFailed || !strings.Contains(r.Detail, "rodou sem analisar nada") || !strings.Contains(r.Detail, `"scanned":[]`) {
		t.Errorf("result = %+v", r)
	}
}

func TestByNameRejectsUnknownScanner(t *testing.T) {
	if _, err := ByName([]string{"gosec", "sonar"}, "", nil, ""); err == nil {
		t.Fatal("scanner desconhecido deveria falhar")
	}
}

// govulncheck-lib-go-auth.jsonl é a saída real do govulncheck v1.8.0 no lib-go-auth (commit
// 2ee1c6a, go.mod com go 1.27.0, toolchain go1.27.0), reduzida aos campos que o adaptador lê.
// São 59 sinais: 49 da stdlib (corrigidos no Go 1.27.2), 5 de golang.org/x/net (v0.60.0),
// 1 de golang.org/x/text (v0.41.0) e 4 de golang.org/x/crypto (v0.55.0 e v0.56.0, e um
// sem versão corrigida, que nunca é descartado).
func TestGovulncheckDropsVulnerabilitiesAlreadyFixedInWhatTheModuleDeclares(t *testing.T) {
	const goMod = `module github.com/acme/lib-go-auth

go %s
%s
require (
	golang.org/x/crypto %s // indirect
	golang.org/x/net %s // indirect
	golang.org/x/text v0.40.0 // indirect
)
%s`
	out := fixture(t, "govulncheck-lib-go-auth.jsonl")
	const total, deps = 59, 10
	cases := []struct {
		name, goDirective, toolchainDirective, xcrypto, xnet, replace, toolchainInUse string
		kept                                                                          int
	}{
		{"go.mod e toolchain em 1.27.0: tudo continua", "1.27.0", "", "v0.54.0", "v0.57.0", "", "go1.27.0", total},
		{"diretiva go 1.27.2 cobre a stdlib", "1.27.2", "", "v0.54.0", "v0.57.0", "", "go1.27.0", deps},
		{"diretiva toolchain go1.27.3 cobre a stdlib", "1.27.0", "toolchain go1.27.3", "v0.54.0", "v0.57.0", "", "go1.27.0", deps},
		{"toolchain em uso go1.27.2 cobre a stdlib", "1.27.0", "", "v0.54.0", "v0.57.0", "", "go1.27.2", deps},
		{"x/net exigido em v0.60.0", "1.27.2", "", "v0.54.0", "v0.60.0", "", "go1.27.0", deps - 5},
		{"x/crypto em v0.55.0 cobre só uma", "1.27.2", "", "v0.55.0", "v0.60.0", "", "go1.27.0", deps - 5 - 1},
		{"tudo atualizado: fica só a vulnerabilidade sem correção", "1.27.2", "", "v0.56.0", "v0.60.0",
			"replace golang.org/x/text => golang.org/x/text v0.41.0", "go1.27.0", 1},
		{"rc anterior à correção não cobre", "1.27rc1", "", "v0.54.0", "v0.57.0", "", "go1.27rc1", total},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			mod := fmt.Sprintf(goMod, tc.goDirective, tc.toolchainDirective, tc.xcrypto, tc.xnet, tc.replace)
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o600); err != nil {
				t.Fatal(err)
			}
			data := bytes.Replace(out, []byte(`"go_version":"go1.27.0"`), []byte(`"go_version":"`+tc.toolchainInUse+`"`), 1)
			sigs, dropped, err := Govulncheck{}.ParseFiltered(data, dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(sigs) != tc.kept || len(sigs)+dropped != total {
				t.Errorf("mantidos %d e descartados %d; quer %d de %d", len(sigs), dropped, tc.kept, total)
			}
		})
	}
}

func TestRunnerReportsFilteredSignals(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m\n\ngo 1.27.2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "govulncheck")
	script := "#!/bin/sh\ncat " + filepath.Join(mustAbs(t, "testdata"), "govulncheck-lib-go-auth.jsonl") + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { //nolint:gosec // script de teste executável
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	r := &Runner{Scanners: []Scanner{Govulncheck{}}}
	sigs, results := r.Run(context.Background(), dir, map[string]bool{"go": true})
	if results[0].Status != "ok" || results[0].Filtered != 49 || results[0].Signals != len(sigs) || len(sigs) != 10 || results[0].Analyzed != 6 || results[0].Unit != "pacotes" {
		t.Errorf("result = %+v, sinais = %d", results[0], len(sigs))
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// Monorepo como o monorepo-poliglota: nenhum go.mod na raiz, módulos Go em subdiretórios. Os
// scanners de Go rodam em cada módulo e os caminhos voltam relativos à raiz do repo.
func TestGoScannersRunInEveryGoModuleOfAMonorepo(t *testing.T) {
	dir := t.TempDir()
	for f, content := range map[string]string{
		"go/go.mod": "module m\n\ngo 1.27\n", "tools/lint/go.mod": "module m\n\ngo 1.27\n", "vendor/x/go.mod": "module m\n",
		"java/pom.xml": "<project/>", ".cache/go.mod": "module m\n",
		"go/internal/a/a.go": "package a\n", "tools/lint/internal/a/a.go": "package a\n", "tools/lint/b/b.go": "package b\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(f))
		_ = os.MkdirAll(filepath.Dir(p), 0o750)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := GoModules(dir); !slices.Equal(got, []string{"go", "tools/lint"}) {
		t.Fatalf("módulos = %v", got)
	}
	// golangci-lint falso: como o real, falha fora de um módulo.
	bin := filepath.Join(t.TempDir(), "golangci-lint")
	script := "#!/bin/sh\n[ -f go.mod ] || { echo 'no go files to analyze' >&2; exit 3; }\n" +
		`echo '{"Issues":[{"FromLinter":"errcheck","Text":"erro ignorado","Severity":"","Pos":{"Filename":"internal/a/a.go","Line":7}}],"Report":{}}'` + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { //nolint:gosec // script de teste executável
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	r := &Runner{Scanners: []Scanner{GolangciLint{}}}
	sigs, results := r.Run(context.Background(), dir, map[string]bool{"go": true})
	if results[0].Status != "ok" || results[0].Signals != 2 || len(sigs) != 2 || results[0].Analyzed != 3 || results[0].Unit != "pacotes" {
		t.Fatalf("result = %+v (go list conta 1 pacote em go/ e 2 em tools/lint/)", results[0])
	}
	if sigs[0].File != "go/internal/a/a.go" || sigs[1].File != "tools/lint/internal/a/a.go" {
		t.Errorf("caminhos = %s, %s", sigs[0].File, sigs[1].File)
	}

	empty := t.TempDir()
	if _, results := r.Run(context.Background(), empty, map[string]bool{"go": true}); results[0].Status != StatusSkipped || results[0].Detail != "nenhum go.mod no repo" {
		t.Errorf("repo sem go.mod = %+v", results[0])
	}
}

// Saída real do gosec sem nenhum problema (monorepo-poliglota depois da EX-12): ok, com a
// contagem de arquivos analisados e zero sinais.
func TestGosecWithNoIssuesIsOK(t *testing.T) {
	out, err := os.ReadFile("testdata/gosec-clean.json")
	if err != nil {
		t.Fatal(err)
	}
	sigs, err := Gosec{}.Parse(out, ".")
	if err != nil || len(sigs) != 0 {
		t.Fatalf("sinais = %v (%v)", sigs, err)
	}
	if n, unit := (Gosec{}).Coverage(out, "."); n == 0 || unit != "arquivos" {
		t.Errorf("cobertura = %d %s", n, unit)
	}
	if _, args := (Gosec{}).Command("."); slices.Contains(args, "-quiet") {
		t.Error("-quiet esconde o relatório quando não há problema")
	}
}
