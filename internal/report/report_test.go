package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dev-danilocordeiro/sentinela/internal/llm"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/signals"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

func TestReportRendersFindingsCostPerAgentAndJudgeDecisions(t *testing.T) {
	d := &Data{
		Run:   db.AnalysisRun{ID: 7, Repo: "acme/app-java-angular", Status: "succeeded", Mode: "full", HeadSha: pgtype.Text{String: "abcdef1234567", Valid: true}},
		Stats: map[string]any{"packages_analyzed": 3, "packages_total": 5, "deepened": 1, "proposed": 2, "published": 1, "discarded": 1, "duplicates": 0},
		Cost:  llm.USD(0.1234), Budget: llm.USD(2),
		Agents: []AgentCost{{Agent: "triager", Models: "claude-haiku-5-5", Calls: 3, Input: 9000, Output: 900, Cost: llm.USD(0.0012)}},
		Findings: []Finding{{
			ListFindingsByRunRow: db.ListFindingsByRunRow{Title: "Trata <erro> ignorado", Severity: "high", Category: "bug", Fingerprint: "abc123def456", Context: "ctx"},
			Ev:                   []signals.Evidence{{File: "a.java", Line: 3, Permalink: "https://github.com/x/y/blob/s/a.java#L3", Snippet: "save()"}},
			Route:                repo.Routing{Team: "time-exemplo", LinearTeam: "EX", LinearProject: "app-java-angular", Rule: "repo"},
		}},
		Judge: []JudgeDecision{{Fingerprint: "abc123def456", Title: "Trata erro", Decision: "publish", Confidence: 0.8, Reason: "evidência clara"}},
	}
	var b bytes.Buffer
	if err := Write(&b, d); err != nil {
		t.Fatal(err)
	}
	html := b.String()
	for _, want := range []string{"Rodada 7", "$0.1234", "claude-haiku-5-5", "Trata &lt;erro&gt; ignorado", "a.java:3", "evidência clara", "dec-publish", "EX / app-java-angular"} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML sem %q", want)
		}
	}
}
