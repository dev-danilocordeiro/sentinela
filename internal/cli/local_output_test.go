package cli

import (
	"strings"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/discovery"
	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
)

// No modo local não há Linear nem Slack: o owners explain não mostra campos vazios deles.
func TestOwnersExplainHidesLinearAndSlackInLocalMode(t *testing.T) {
	res := repo.Resolution{Repo: "acme/mono", Path: "Makefile"}
	res.Team, res.Rule = "backend", "config:api/**"
	var local, full strings.Builder
	printResolution(&local, res, true)
	printResolution(&full, res, false)
	for _, gone := range []string{"linear:", "projeto:", "assignee:", "canal:", "menção:"} {
		if strings.Contains(local.String(), gone) {
			t.Errorf("modo local mostra %q:\n%s", gone, local.String())
		}
		if !strings.Contains(full.String(), gone) {
			t.Errorf("modo Linear perdeu %q:\n%s", gone, full.String())
		}
	}
	if !strings.Contains(local.String(), "time:            backend") || !strings.Contains(local.String(), "não se aplicam no modo local") {
		t.Errorf("modo local:\n%s", local.String())
	}
}

// No modo local o achado vai para o relatório local, não para uma issue.
func TestLocalScanOutputSaysFindingNotIssue(t *testing.T) {
	res := discovery.Result{Stats: discovery.Stats{Issues: &linear.PublishResult{Issues: []linear.PublishedIssue{{Identifier: "F-1", URL: "reports/local/acme__mono.md#f-1", Title: "Valida o fuso"}}}}}
	var local, full strings.Builder
	printPublish(&local, res, true)
	printPublish(&full, res, false)
	if !strings.HasPrefix(local.String(), "achado F-1 reports/local/acme__mono.md#f-1: Valida o fuso") {
		t.Errorf("modo local: %q", local.String())
	}
	if !strings.HasPrefix(full.String(), "issue F-1 ") {
		t.Errorf("modo Linear: %q", full.String())
	}
}

// Sem orçamento em dólar (assinatura), o report não mostra "de $0".
func TestReportCostLineWithoutDollarBudget(t *testing.T) {
	if got := costLine("$5.937269", "$0.000000", true); got != "custo $5.937269 (equivalente em preço de lista; assinatura, sem orçamento em dólar)" {
		t.Errorf("assinatura: %q", got)
	}
	if got := costLine("$2.000000", "$10.000000", false); got != "custo $2.000000 de $10.000000" {
		t.Errorf("API: %q", got)
	}
}
