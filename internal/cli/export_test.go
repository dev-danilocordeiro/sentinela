package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// scripts/exportar.sh falha quando um termo proibido aparece na saída (trecho ou palavra inteira)
// e passa quando a tag está limpa, com o módulo trocado e os arquivos da exclusão fora (D-01).
func TestExportFailsOnForbiddenTerms(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("sem git")
	}
	script, err := os.ReadFile(filepath.Join("..", "..", "scripts", "exportar.sh"))
	if os.IsNotExist(err) {
		t.Skip("sem scripts/exportar.sh: é uma cópia exportada")
	}
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil { //nolint:gosec // script e fixtures de teste
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write("scripts/exportar.sh", string(script))
	write("scripts/termos-proibidos.txt", "# teste\nsegredo-pessoal\npalavra:Xyz\n")
	write("go.mod", "module example.com/pessoal/sentinela\n\ngo 1.27.0\n")
	write("main.go", "package main\n\nimport _ \"example.com/pessoal/sentinela/x\"\n\nfunc main() {}\n")
	write("Sentinela.html", "segredo-pessoal\n")                                // na lista de exclusão
	write("docs/ok.md", "Xyzzy não é a palavra proibida; marcou também não.\n") // palavra inteira só
	git("init", "-q", "-b", "main")
	git("add", "-A")
	git("commit", "-q", "-m", "limpo")
	git("tag", "limpo")
	write("docs/vaza.md", "contato: Segredo-Pessoal e xyz\n")
	git("add", "-A")
	git("commit", "-q", "-m", "vaza")
	git("tag", "vaza")

	run := func(tag string, extra ...string) (string, error) {
		out := filepath.Join(t.TempDir(), "saida")
		args := append([]string{filepath.Join(repo, "scripts", "exportar.sh"), "--tag", tag, "--modulo", "github.com/acme/sentinela", "--saida", out, "--sem-testes"}, extra...)
		cmd := exec.Command("sh", args...)
		b, err := cmd.CombinedOutput()
		if err == nil {
			mod, _ := os.ReadFile(filepath.Join(out, "go.mod"))
			main, _ := os.ReadFile(filepath.Join(out, "main.go"))
			if !strings.Contains(string(mod), "module github.com/acme/sentinela") || !strings.Contains(string(main), "github.com/acme/sentinela/x") {
				t.Errorf("módulo não trocado:\n%s\n%s", mod, main)
			}
			for _, gone := range []string{"Sentinela.html", "scripts/exportar.sh", "scripts/termos-proibidos.txt"} {
				if _, err := os.Stat(filepath.Join(out, gone)); err == nil {
					t.Errorf("%s deveria ficar fora da exportação", gone)
				}
			}
		}
		return string(b), err
	}
	if out, err := run("limpo"); err != nil {
		t.Fatalf("tag limpa falhou: %v\n%s", err, out)
	}
	out, err := run("vaza")
	if err == nil || !strings.Contains(out, "termo proibido") || !strings.Contains(out, "docs/vaza.md") {
		t.Fatalf("tag com termo proibido deveria falhar apontando o arquivo: %v\n%s", err, out)
	}
	// --allow drops only the named term: the other one still blocks the export.
	out, err = run("vaza", "--allow", "SEGREDO-PESSOAL")
	if err == nil || !strings.Contains(out, "termo proibido") || !strings.Contains(out, "1 ocorrência") {
		t.Fatalf("--allow should drop only the allowed term: %v\n%s", err, out)
	}
	if out, err := run("vaza", "--allow", "segredo-pessoal", "--allow", "Xyz"); err != nil {
		t.Fatalf("with both terms allowed the export should pass: %v\n%s", err, out)
	}
	if out, err := run("nao-existe"); err == nil || !strings.Contains(out, "não existe") {
		t.Errorf("tag inexistente: %v\n%s", err, out)
	}
}
