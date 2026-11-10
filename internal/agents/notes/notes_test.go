package notes

import (
	"reflect"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/agents/agentstest"
)

// input usa as três notas reais do PR #3 (EX-12) do monorepo-poliglota.
func input() Input {
	return Input{
		Repo: "acme/monorepo-poliglota", MaxFindings: 5,
		Execution: Execution{Issue: "EX-12", Title: "Configura timeouts no servidor HTTP", PRURL: "https://github.com/acme/monorepo-poliglota/pull/3",
			HeadSHA: "77edbe0", ScopePaths: []string{"go/cmd/api"}, ProposedFix: "Usar http.Server com timeouts."},
		Notes: []Note{
			{ID: "note:1", Lens: "security", Round: 1, Text: "`go/cmd/api/main.go:24-44`: os handlers não limitam o tamanho do corpo com `http.MaxBytesReader`."},
			{ID: "note:2", Lens: "security", Round: 1, Text: "`go/cmd/api/main.go:38,40`: `err.Error()` vai para o cliente na resposta."},
			{ID: "note:3", Lens: "reliability", Round: 1, Text: "`go/cmd/api/main.go:47`: sem shutdown gracioso, então o SIGTERM derruba as requisições em andamento."},
		},
		Excerpts: []Excerpt{{File: "go/cmd/api/main.go", StartLine: 12, Code: "package main"}},
	}
}

func TestRequestMatchesGolden(t *testing.T) {
	req, err := Request(input(), Options{Model: "claude-sonnet-5-5"})
	if err != nil {
		t.Fatal(err)
	}
	agentstest.Golden(t, "request", agentstest.View(req))
}

func TestParseKeepsOnlyFindingsThatCiteKnownNotes(t *testing.T) {
	out, warnings, err := Parse(agentstest.Recorded(t, "pr3"), input())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Findings) != 2 || len(warnings) != 1 {
		t.Fatalf("achados = %d, avisos = %v (o achado que cita note:42 sai)", len(out.Findings), warnings)
	}
	if got := out.Findings[1].SignalRefs; !reflect.DeepEqual(got, []string{"note:2"}) {
		t.Errorf("refs = %v (note:9 não existe)", got)
	}
	want := []Ignored{{ID: "note:3", Reason: "o comportamento é o mesmo de antes; sem defeito atual"}}
	if !reflect.DeepEqual(out.Ignored, want) {
		t.Errorf("notas ignoradas = %+v (toda nota tem destino)", out.Ignored)
	}
}

func TestIsNoteAndRefs(t *testing.T) {
	for item, want := range map[string]bool{
		"(não bloqueia) `go/cmd/api/main.go:47`: sem shutdown gracioso": true,
		"(fora do escopo) go/cmd/api/main.go:24: corpo sem limite":      true,
		"(Não Bloqueia) maiúsculas":                                     true,
		"nenhum":                                                        false,
		"go/cmd/api/main.go:3 falta tratar o erro":                      false,
	} {
		if _, ok := IsNote(item); ok != want {
			t.Errorf("IsNote(%q) = %v", item, ok)
		}
	}
	got := Refs("`go/cmd/api/main.go:24-44` e `main.go:38,40` e go/cmd/api/main_test.go:8")
	want := []Ref{{"go/cmd/api/main.go", 24, 44}, {"main.go", 38, 40}, {"go/cmd/api/main_test.go", 8, 8}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("refs = %+v", got)
	}
}
