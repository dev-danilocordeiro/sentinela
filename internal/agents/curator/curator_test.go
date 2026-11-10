package curator

import (
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/agents/agentstest"
)

func input() Input {
	return Input{
		Repo: "acme/app-java-angular", DefaultMinConfidence: 0.6, MaxExamples: 2,
		Outcomes: []Outcome{{
			FindingID: 7, Module: "backend/controller", Category: "security", Severity: "high",
			Title: "Exige autorização no endpoint de exclusão de URL", File: "backend/controller/UrlController.java",
			Outcome: "rejected", Reason: "O app não tem login; é decisão de produto.", AnalystConfidence: 0.7, JudgeConfidence: 0.7,
		}},
	}
}

func TestRequestMatchesGolden(t *testing.T) {
	req, err := Request(input(), Options{Model: "claude-haiku-5-5"})
	if err != nil {
		t.Fatal(err)
	}
	agentstest.Golden(t, "request", agentstest.View(req))
}

func TestParseKeepsOnlyWhatTheHouseRulesAllow(t *testing.T) {
	out, notes, err := Parse(agentstest.Recorded(t, "curation"), input())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Examples) != 1 || out.Examples[0].FindingID != 7 {
		t.Errorf("exemplos = %+v (achado 99 não estava na entrada)", out.Examples)
	}
	if len(out.Suppressions) != 1 || out.Suppressions[0].Pattern != "backend/generated/**" {
		t.Errorf("supressões = %+v (\"**\" cobre o repo inteiro)", out.Suppressions)
	}
	if len(out.Thresholds) != 1 || out.Thresholds[0].MinConfidence != MaxThreshold {
		t.Errorf("limiares = %+v (0,99 deveria virar 0,95)", out.Thresholds)
	}
	if len(notes) != 3 {
		t.Errorf("notas = %v", notes)
	}
}
