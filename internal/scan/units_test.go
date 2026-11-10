package scan

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
)

// jvmProbe é um scanner falso que roda por módulo Gradle ou Maven e devolve um sinal por unidade.
type jvmProbe struct{ dirs *[]string }

func (jvmProbe) Name() string                          { return "jvm-probe" }
func (jvmProbe) Applies(l map[string]bool) bool        { return l["java"] || l["kotlin"] }
func (jvmProbe) Command(string) (string, []string)     { return "sh", []string{"-c", "pwd"} }
func (jvmProbe) Coverage([]byte, string) (int, string) { return 1, "módulos" }
func (p jvmProbe) Parse(out []byte, _ string) ([]Signal, error) {
	*p.dirs = append(*p.dirs, filepath.Base(strings.TrimSpace(string(out))))
	return []Signal{{Scanner: "jvm-probe", File: "src/main/java/A.java", Line: 1, Message: "x"}}, nil
}
func (jvmProbe) runsIn(u repo.BuildUnit) bool { return u.JVM() }
func (jvmProbe) noUnits() string              { return "nenhum módulo Gradle ou Maven no repo" }

// Fase C, passo 3: scanner por unidade de build, generalizando o perGoModule.
func TestScannersRunOncePerBuildUnitOfTheirKind(t *testing.T) {
	dir := t.TempDir()
	for f, content := range map[string]string{
		"api/pom.xml": "<project/>", "api/domain/pom.xml": "<project/>", "web/package.json": "{}",
	} {
		p := filepath.Join(dir, filepath.FromSlash(f))
		_ = os.MkdirAll(filepath.Dir(p), 0o750)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var ran []string
	r := &Runner{Scanners: []Scanner{jvmProbe{dirs: &ran}}}
	sigs, results := r.Run(context.Background(), dir, map[string]bool{"java": true})
	if results[0].Status != StatusOK || results[0].Units != 2 || len(sigs) != 2 {
		t.Fatalf("result = %+v", results[0])
	}
	if !slices.Equal(ran, []string{"api", "domain"}) {
		t.Errorf("rodou em %v, quer api e api/domain", ran)
	}
	if sigs[0].File != "api/src/main/java/A.java" || sigs[1].File != "api/domain/src/main/java/A.java" {
		t.Errorf("caminhos = %s, %s", sigs[0].File, sigs[1].File)
	}

	empty := t.TempDir()
	_ = os.WriteFile(filepath.Join(empty, "A.java"), []byte("class A {}"), 0o600)
	_, results = r.Run(context.Background(), empty, map[string]bool{"java": true})
	if results[0].Status != StatusSkipped || results[0].Detail != "nenhum módulo Gradle ou Maven no repo" {
		t.Errorf("sem unidade: %+v", results[0])
	}
}

// Fase C, passo 5: o semgrep roda com a config de base mais as das linguagens do repo.
func TestSemgrepAddsTheConfigsOfTheRepoLanguages(t *testing.T) {
	s := Semgrep{Config: "p/default", Languages: map[string][]string{
		"java": {"p/java"}, "kotlin": {"p/kotlin"}, "typescript": {"p/typescript", "p/react"},
	}}
	got := s.forLanguages(map[string]bool{"java": true, "typescript": true, "config": true})
	_, args := got.Command(".")
	joined := strings.Join(args, " ")
	for _, w := range []string{"--config p/default", "--config p/java", "--config p/typescript", "--config p/react"} {
		if !strings.Contains(joined, w) {
			t.Errorf("args sem %q: %s", w, joined)
		}
	}
	if n := got.(noter).note(nil, "."); n != "regras: p/default, p/java, p/typescript, p/react" {
		t.Errorf("nota = %q", n)
	}
	if strings.Contains(joined, "p/kotlin") {
		t.Errorf("Kotlin não está no repo: %s", joined)
	}
	if _, args := (Semgrep{}).forLanguages(map[string]bool{"go": true}).Command("."); strings.Join(args, " ") != "scan --json --quiet --metrics off --config p/default" {
		t.Errorf("sem linguagem mapeada, só a base: %v", args)
	}
}

// Web e BFF em TypeScript no mesmo repo: os pacotes de TS, JS, Node, React e Express do padrão,
// sem repetir (p/nodejs e p/expressjs valem para as duas linguagens). Não há pacote de Nest no
// registro (p/nestjs existe e está vazio, conferido em 2026-10-18).
func TestSemgrepDefaultsForAWebAndBFFInTypeScript(t *testing.T) {
	s := Semgrep{Config: "p/default", Languages: config.DefaultSemgrepLanguages}
	got := s.forLanguages(map[string]bool{"typescript": true, "javascript": true, "config": true})
	if n := got.(noter).note(nil, "."); n != "regras: p/default, p/javascript, p/nodejs, p/expressjs, p/typescript, p/react" {
		t.Errorf("nota = %q", n)
	}
	if n := s.forLanguages(map[string]bool{"typescript": true}).(noter).note(nil, "."); n != "regras: p/default, p/typescript, p/react, p/nodejs, p/expressjs" {
		t.Errorf("só TypeScript: %q", n)
	}
}
