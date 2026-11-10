package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// D-16 da revisão de prontidão: o CI roda os testes de integração num Postgres com pgvector, o
// lint e a checagem de termos proibidos da exportação.
func TestCIWorkflowRunsIntegrationTestsLintAndForbiddenTerms(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Jobs map[string]struct {
			Services map[string]struct {
				Image string `yaml:"image"`
			} `yaml:"services"`
			Env   map[string]string `yaml:"env"`
			Steps []struct {
				Name string         `yaml:"name"`
				Uses string         `yaml:"uses"`
				Run  string         `yaml:"run"`
				With map[string]any `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatal(err)
	}
	var pg, testDB, tests, lint, terms bool
	for _, j := range wf.Jobs {
		for _, s := range j.Services {
			pg = pg || strings.HasPrefix(s.Image, "pgvector/pgvector:pg16")
		}
		testDB = testDB || strings.Contains(j.Env["SENTINELA_TEST_DATABASE_URL"], "sentinela_test")
		for _, s := range j.Steps {
			tests = tests || strings.Contains(s.Run, "go test -p 1 ./...")
			lint = lint || strings.HasPrefix(s.Uses, "golangci/golangci-lint-action@")
			terms = terms || strings.Contains(s.Run, "scripts/exportar.sh")
		}
	}
	if !pg || !testDB || !tests || !lint || !terms {
		t.Errorf("CI incompleto: postgres+pgvector %v, banco de teste %v, go test -p 1 %v, golangci-lint %v, termos proibidos %v", pg, testDB, tests, lint, terms)
	}
}
