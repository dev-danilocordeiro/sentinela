package linear_test

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// Issue de um repo sem AGENTS.md nem CLAUDE.md (na raiz ou no workspace do achado, que a função
// recebe pelo escopo e pela evidência) ganha a linha avisando que a correção só roda
// depois que o arquivo existir; com o arquivo, ou sem clone para conferir, não.
func TestPublishWarnsWhenTheRepoHasNoCommandsDoc(t *testing.T) {
	for _, c := range []struct {
		name  string
		doc   string
		known bool
		warn  bool
	}{
		{"sem arquivo", "", true, true},
		{"com AGENTS.md", "AGENTS.md", true, false},
		{"sem clone", "", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t)
			e.cfg.Labels.Trigger = "sentinela-fix"
			e.pub.CommandsDoc = func(name string, scope []string) (string, bool) {
				if name != "acme/mono" || !slices.Contains(scope, "libs/x/a.go") {
					t.Errorf("CommandsDoc(%q, %q)", name, scope)
				}
				return c.doc, c.known
			}
			e.addFinding(t, "agentsmd0001", "libs/x/a.go", "medium")
			if _, err := e.pub.Publish(context.Background(), "acme/mono", 0, e.res); err != nil {
				t.Fatal(err)
			}
			desc := e.fake.Issues[len(e.fake.Issues)-1].Description
			note := "o repo `acme/mono` não tem AGENTS.md nem CLAUDE.md na raiz nem no workspace do achado. A correção pelo Sentinela (label `sentinela-fix`) só roda depois que esse arquivo existir"
			if got := strings.Contains(desc, note); got != c.warn {
				t.Errorf("aviso na issue = %v, quer %v:\n%s", got, c.warn, desc)
			}
			if c.warn && strings.Index(desc, note) > strings.LastIndex(desc, "\n---\n") {
				t.Errorf("aviso depois do rodapé:\n%s", desc)
			}
		})
	}
}
