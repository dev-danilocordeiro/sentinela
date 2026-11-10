package code

import (
	"strconv"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/signals"
)

func TestLandmarksAreValidEvidenceForTheArchitect(t *testing.T) {
	pkgs := []repo.Package{
		{Path: "backend/src/main/java/com/example/app/service", Files: []repo.File{{Path: service, Size: 1}}},
		{Path: ".", Files: []repo.File{{Path: "docker-compose.yml", Size: 1}}},
	}
	c := &EvidenceChecker{Dir: appJava}
	want := map[string]bool{service + ":37": false, "docker-compose.yml:7": false}
	for _, p := range pkgs {
		marks := landmarks(appJava, p, landmarksTotal)
		if len(marks) == 0 || len(marks) > landmarksPerPackage {
			t.Fatalf("%s: %d landmarks", p.Path, len(marks))
		}
		for _, m := range marks {
			if why := c.invalid(signals.Evidence{File: m.File, Line: m.Line, Snippet: m.Code}); why != "" {
				t.Errorf("landmark %s:%d %q não passa na validação: %s", m.File, m.Line, m.Code, why)
			}
			if _, ok := want[m.File+":"+strconv.Itoa(m.Line)]; ok {
				want[m.File+":"+strconv.Itoa(m.Line)] = true
			}
		}
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("faltou o landmark %s (o mapa em memória e o AOF do Redis, o tema da EX-9)", k)
		}
	}
	if got := landmarks(appJava, pkgs[0], 3); len(got) != 3 {
		t.Errorf("orçamento total ignorado: %d", len(got))
	}
}
