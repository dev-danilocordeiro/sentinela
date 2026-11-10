package architect

import (
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/agents/agentstest"
)

func TestRequestMatchesGolden(t *testing.T) {
	req, err := Request(Input{
		Repo: "acme/app-java-angular",
		Packages: []Package{
			{Path: "app/controller", Language: "java", OwnerTeam: "time-exemplo", Summary: "API HTTP", Landmarks: []Landmark{{File: "app/controller/UrlController.java", Line: 3, Code: "import app.model.UrlData;"}}},
			{Path: "app/model", Language: "java", OwnerTeam: "time-exemplo", Summary: "Modelo gravado no Redis"},
		},
		Edges:       []Edge{{From: "app/controller", To: "app/model"}},
		MaxFindings: 5,
	}, Options{Model: "claude-sonnet-5-5"})
	if err != nil {
		t.Fatal(err)
	}
	agentstest.Golden(t, "request", agentstest.View(req))
}

func TestParseRecordedResponse(t *testing.T) {
	out, err := Parse(agentstest.Recorded(t, "system"), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Findings) != 1 || len(out.Findings[0].AffectedModules) != 1 {
		t.Errorf("out = %+v", out)
	}
}
