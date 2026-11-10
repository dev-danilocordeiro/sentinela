package analyst

import (
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/agents"
	"github.com/dev-danilocordeiro/sentinela/internal/agents/agentstest"
)

func TestRequestMatchesGolden(t *testing.T) {
	req, _, err := Request(Input{
		Repo: "acme/app-java-angular", Package: "src", Language: "java", OwnerTeam: "time-exemplo",
		Files:       []agents.File{{Path: "src/UrlService.java", Content: agents.NumberLines("class UrlService {\n  void shorten() {\n    redis.save(url);\n  }\n}\n")}},
		Triage:      Triage{Category: "bug", Reason: "save() ignorado"},
		MaxFindings: 5,
	}, Options{Model: "claude-sonnet-5-5", ContextTokens: 150_000})
	if err != nil {
		t.Fatal(err)
	}
	agentstest.Golden(t, "request", agentstest.View(req))
}

func TestParseRecordedResponse(t *testing.T) {
	out, err := Parse(agentstest.Recorded(t, "service"), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Findings) != 1 || out.Findings[0].Evidence[0].Line != 3 || out.Findings[0].ProblemKey == "" {
		t.Errorf("out = %+v", out)
	}
}

func TestParseCapsFindingsAndRejectsBadConfidence(t *testing.T) {
	resp := agentstest.Recorded(t, "service")
	out, err := Parse(resp, 0)
	if err != nil || len(out.Findings) != 1 {
		t.Fatalf("sem limite: %v %v", out, err)
	}
	resp.Text = `{"findings":[{"category":"bug","severity":"high","confidence":1.4,"title":"t","context":"c","problem_key":"k","evidence":[],"proposed_fix":"f","acceptance_criteria":[],"scope_paths":[],"signal_refs":[]}]}`
	if _, err := Parse(resp, 5); err == nil {
		t.Error("confidence acima de 1 deveria falhar")
	}
}
