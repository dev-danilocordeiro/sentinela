package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A versão mínima do Go que os docs pedem é a do go.mod, e a imagem compila com ela (D-14 da
// revisão de prontidão: os docs pediam Go 1.23+ com o go.mod em 1.27.0).
func TestDocsAskForTheGoVersionOfGoMod(t *testing.T) {
	root := filepath.Join("..", "..")
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^go (\d+)\.(\d+)`).FindStringSubmatch(string(mod))
	if m == nil {
		t.Fatal("go.mod sem diretiva go")
	}
	want := m[1] + "." + m[2]
	files := []string{"README.md", "CLAUDE.md", "SENTINELA_BUILD_PROMPT.md"}
	docs, _ := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	for _, d := range docs {
		rel, _ := filepath.Rel(root, d)
		files = append(files, rel)
	}
	requirement := regexp.MustCompile(`Go (\d+\.\d+)(\+| ou mais novo)`)
	found := 0
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			continue
		}
		for _, r := range requirement.FindAllStringSubmatch(string(b), -1) {
			found++
			if r[1] != want {
				t.Errorf("%s pede %q; o go.mod pede Go %s", f, r[0], want)
			}
		}
	}
	if found == 0 {
		t.Error("nenhum doc diz a versão mínima do Go")
	}
	docker, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(docker), "FROM golang:"+want+" ") {
		t.Errorf("o Dockerfile não compila com o Go %s do go.mod", want)
	}
}
