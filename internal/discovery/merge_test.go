package discovery

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/pgvector/pgvector-go"

	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

func (e *env) finding(t *testing.T, fp, title string) int64 {
	t.Helper()
	routing, _ := json.Marshal(map[string]string{"team": "core"})
	id, err := e.st.Q.InsertFinding(context.Background(), db.InsertFindingParams{
		Fingerprint: fp, Source: "code_analysis", Repo: "acme/app-java-angular", Module: "backend/service", Owners: []string{}, Routing: routing,
		Category: "bug", Severity: "medium", Confidence: 0.7, Title: title, Evidence: []byte("[]"), Signals: []byte("[]"),
		AcceptanceCriteria: []string{}, ScopePaths: []string{}, RelatedModules: []string{}, Embedding: pgvector.NewVector(make([]float32, 1024)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *env) publish(t *testing.T, findingID int64, identifier string) {
	t.Helper()
	if _, err := e.st.Q.InsertLinearIssue(context.Background(), db.InsertLinearIssueParams{
		FindingID: findingID, Role: "primary", Team: "time-exemplo", Module: "backend/service", LinearTeam: "EX",
		LinearID: "lin-" + identifier, Identifier: identifier, Url: "https://linear.app/exemplo/issue/" + identifier, StateName: "Backlog", StateType: "backlog",
	}); err != nil {
		t.Fatal(err)
	}
}

// O par real do app-java-angular: no rejulgamento, EX-6 se mesclava na EX-8 e EX-8 na EX-6. A regra
// é determinística: entre dois publicados, a issue mais antiga (EX-6) absorve a mais nova.
func TestMergeDirectionIsDeterministicForTheRealHAY6HAY8Pair(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	ex6 := e.finding(t, "f1d40bb6ddcf", "Impede que redirect concorrente recacheie URL deletada")
	ex8 := e.finding(t, "9f8c1e90f2a7", "Impede redirecionamento de link desativado quando a remoção do cache falha")
	e.publish(t, ex6, "EX-6")
	e.publish(t, ex8, "EX-8")
	for _, pair := range [][2]string{{"f1d40bb6ddcf", "9f8c1e90f2a7"}, {"9f8c1e90f2a7", "f1d40bb6ddcf"}} {
		mc, err := e.runner.MergeDirection(ctx, pair[0], pair[1])
		if err != nil {
			t.Fatal(err)
		}
		if mc.Absorber != "f1d40bb6ddcf" || mc.Absorbed != "9f8c1e90f2a7" {
			t.Errorf("%v: %+v (EX-6, a mais antiga, absorve a EX-8)", pair, mc)
		}
	}
	// Publicado absorve não publicado, em qualquer ordem; candidato novo é sempre absorvido.
	pend := e.finding(t, "aaaaaaaaaaaa", "Pendente")
	_ = pend
	for _, c := range []struct{ a, b, want string }{
		{"aaaaaaaaaaaa", "9f8c1e90f2a7", "9f8c1e90f2a7"},
		{"9f8c1e90f2a7", "aaaaaaaaaaaa", "9f8c1e90f2a7"},
		{"bbbbbbbbbbbb", "aaaaaaaaaaaa", "aaaaaaaaaaaa"}, // nenhum publicado: o gravado (mais antigo) absorve o candidato
	} {
		mc, err := e.runner.MergeDirection(ctx, c.a, c.b)
		if err != nil || mc.Absorber != c.want {
			t.Errorf("%s x %s = %+v (%v), quer %s", c.a, c.b, mc, err, c.want)
		}
	}
}
