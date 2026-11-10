package cli

import (
	"strings"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/discovery"
	"github.com/dev-danilocordeiro/sentinela/internal/linear"
)

// M8: no scan --all, o resumo de cada repo diz que a publicação ficou para o fim, e o resumo
// da publicação mostra os adiados por time, com a severidade.
func TestRoundSummaryShowsDeferredByTeamAndSeverity(t *testing.T) {
	is := linear.PublishResult{
		Issues:           []linear.PublishedIssue{{Identifier: "EX-1", URL: "https://x/EX-1", Title: "Corrigir", Category: "bug", Severity: "critical", Team: "plataforma"}},
		Deferred:         map[string]int{"plataforma": 4, "financeiro": 1},
		DeferredSeverity: map[string]map[string]int{"plataforma": {"high": 1, "low": 3}, "financeiro": {"medium": 1}},
	}
	got := roundSummary([]string{"a/b", "a/c"}, is, nil)
	for _, want := range []string{
		"publicação do scan --all* (2 repo(s), limite por time somado entre os repos)",
		"Issues novas (1):",
		"Adiadas pelo limite por time: financeiro 1 (medium 1) · plataforma 4 (high 1, low 3)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("resumo sem %q:\n%s", want, got)
		}
	}
	repo := runSummary("a/b", discovery.Result{RunID: 7, Stats: discovery.Stats{Issues: &linear.PublishResult{Postponed: true}}})
	if !strings.Contains(repo, "Publicação no fim do scan --all") || strings.Contains(repo, "Nenhuma issue nova") {
		t.Errorf("resumo do repo:\n%s", repo)
	}
}
