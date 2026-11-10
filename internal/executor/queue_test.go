package executor

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/pgvector/pgvector-go"

	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

func TestOverlapComparesFilesDirectoriesAndGlobs(t *testing.T) {
	cases := []struct {
		a, b   []string
		ok     bool
		common string
	}{
		{[]string{"cmd/api/main.go"}, []string{"cmd/api/main.go"}, true, "cmd/api/main.go"},
		{[]string{"cmd/api"}, []string{"cmd/api/main.go"}, true, "cmd/api/main.go"},
		{[]string{"./cmd/api/"}, []string{"cmd/api/handler.go"}, true, "cmd/api/handler.go"},
		{[]string{"cmd/api"}, []string{"cmd/apigw/main.go"}, false, ""},
		{[]string{"cmd/api"}, []string{"internal/store"}, false, ""},
		{[]string{"services/billing/**"}, []string{"services/billing/charge.go"}, true, "services/billing/charge.go"},
		{[]string{"services/billing/**"}, []string{"services"}, true, "services/billing/**"},
		{[]string{"**/*.sql"}, []string{"db/migration/V1.sql"}, true, "db/migration/V1.sql"},
		{[]string{"services/billing/**"}, []string{"services/orders"}, false, ""},
		{nil, []string{"anything/at/all.go"}, true, "anything/at/all.go"},
		{[]string{"."}, nil, true, "(repo inteiro)"},
	}
	for _, c := range cases {
		common, ok := overlap(c.a, c.b)
		if ok != c.ok || common != c.common {
			t.Errorf("overlap(%v, %v) = %q, %v; quer %q, %v", c.a, c.b, common, ok, c.common, c.ok)
		}
	}
}

func TestPlanQueue(t *testing.T) {
	api := []string{"cmd/api"}
	store := []string{"internal/store"}
	q := func(id, finding int64, paths []string) QueueJob {
		return QueueJob{ID: id, FindingID: finding, Repo: "acme/svc", Issue: "EX-" + string(rune('0'+id)), Status: "queued", Ready: true, Paths: paths}
	}
	cases := []struct {
		name      string
		jobs      []QueueJob
		claimable []int64
		waits     map[int64]string // trecho esperado do motivo
	}{
		{
			name:      "sem sobreposição: todos podem rodar em paralelo",
			jobs:      []QueueJob{q(1, 1, api), q(2, 2, store)},
			claimable: []int64{1, 2},
		},
		{
			name:      "dois na fila com arquivo em comum: o segundo espera o primeiro",
			jobs:      []QueueJob{q(1, 1, api), q(2, 2, []string{"cmd/api/main.go"}), q(3, 3, store)},
			claimable: []int64{1, 3},
			waits:     map[int64]string{2: "aguarda job 1 (EX-1), na fila antes: caminho em comum cmd/api/main.go"},
		},
		{
			name: "job rodando segura os arquivos",
			jobs: []QueueJob{
				{ID: 1, FindingID: 1, Repo: "acme/svc", Issue: "EX-1", Status: "running", Paths: api},
				q(2, 2, api),
			},
			waits: map[int64]string{2: "em execução"},
		},
		{
			name: "PR aberto segura os arquivos até ser mergeado ou fechado",
			jobs: []QueueJob{
				{ID: 1, FindingID: 1, Repo: "acme/svc", Issue: "EX-1", Status: "succeeded", PRNumber: 7, PROpen: true, Paths: api},
				q(2, 2, api), q(3, 3, store),
			},
			claimable: []int64{3},
			waits:     map[int64]string{2: "PR #7 aberto"},
		},
		{
			name: "PR fechado ou job sem PR não segura nada",
			jobs: []QueueJob{
				{ID: 1, FindingID: 1, Repo: "acme/svc", Status: "succeeded", PRNumber: 7, Paths: api},
				{ID: 2, FindingID: 2, Repo: "acme/svc", Status: "failed", Paths: api},
				q(3, 3, api),
			},
			claimable: []int64{3},
		},
		{
			name: "outro repo não conflita",
			jobs: []QueueJob{
				{ID: 1, FindingID: 1, Repo: "acme/outro", Status: "running", Paths: api},
				q(2, 2, api),
			},
			claimable: []int64{2},
		},
		{
			name: "rework do mesmo achado não espera pelo próprio PR nem pelos que esperam por ele",
			jobs: []QueueJob{
				{ID: 1, FindingID: 1, Repo: "acme/svc", Issue: "EX-1", Status: "succeeded", PRNumber: 7, PROpen: true, Paths: api},
				q(2, 2, api), // chegou antes do rework e espera o PR #7
				{ID: 3, FindingID: 1, Repo: "acme/svc", Issue: "EX-1", Status: "queued", Ready: true, PRNumber: 7, PROpen: true, FindingHasOpenPR: true, Paths: api},
			},
			claimable: []int64{3},
			waits:     map[int64]string{2: "PR #7 aberto"},
		},
		{
			name:  "job em espera de backoff ainda segura a ordem dos que vêm depois",
			jobs:  []QueueJob{{ID: 1, FindingID: 1, Repo: "acme/svc", Issue: "EX-1", Status: "queued", Paths: api}, q(2, 2, api)},
			waits: map[int64]string{2: "na fila antes"},
		},
		{
			name:      "achado sem caminhos conflita com tudo do repo",
			jobs:      []QueueJob{q(1, 1, nil), q(2, 2, store)},
			claimable: []int64{1},
			waits:     map[int64]string{2: "caminho em comum internal/store"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan := PlanQueue(c.jobs)
			if fmtIDs(plan.Claimable) != fmtIDs(c.claimable) {
				t.Errorf("claimable = %v, quer %v", plan.Claimable, c.claimable)
			}
			if len(plan.Waits) != len(c.waits) {
				t.Errorf("waits = %v, quer %v", plan.Waits, c.waits)
			}
			for id, want := range c.waits {
				if !strings.Contains(plan.Waits[id], want) {
					t.Errorf("motivo do job %d = %q, quer conter %q", id, plan.Waits[id], want)
				}
			}
		})
	}
}

func fmtIDs(ids []int64) string {
	b, _ := json.Marshal(ids)
	if string(b) == "null" {
		return "[]"
	}
	return string(b)
}

// addFinding publica mais um achado do acme/svc com issue no Linear falso.
func addFinding(t *testing.T, e *env, fp, title string, scope []string, file string) string {
	t.Helper()
	ctx := context.Background()
	lc := e.w.Linear
	issue, err := lc.CreateIssue(ctx, linear.IssueInput{TeamID: "team-ex", Title: title, Description: "Corpo.\n\n`sentinela-fingerprint: " + fp + "`"})
	if err != nil {
		t.Fatal(err)
	}
	routing, _ := json.Marshal(repo.Routing{Team: "core", LinearTeam: "EX", LinearProject: "core", Rule: "repo"})
	ev, _ := json.Marshal([]map[string]any{{"file": file, "line": 1, "snippet": "x"}})
	fid, err := e.st.Q.InsertFinding(ctx, db.InsertFindingParams{
		Fingerprint: fp, Source: "code_analysis", Repo: "acme/svc", Module: scope[0], Owners: []string{"core"}, Routing: routing,
		Category: "bug", Severity: "medium", Confidence: 0.8, Title: title, Context: "c", Evidence: ev, Signals: []byte("[]"),
		ProposedFix: "f", AcceptanceCriteria: []string{"ok"}, ScopePaths: scope, RelatedModules: []string{},
		Embedding: pgvector.NewVector(make([]float32, 1024)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Q.InsertLinearIssue(ctx, db.InsertLinearIssueParams{
		FindingID: fid, Role: "primary", Team: "core", Module: scope[0], LinearTeam: "EX", LinearID: issue.ID,
		Identifier: issue.Identifier, Url: issue.URL, StateName: "Backlog", StateType: "backlog",
	}); err != nil {
		t.Fatal(err)
	}
	return issue.Identifier
}

// Fila consciente de conflitos (ADR 0031): EX-2 mexe no mesmo diretório que EX-1 e espera o PR
// dela ser mergeado, com o motivo gravado; EX-3, em outro pacote, roda enquanto isso. Depois do
// merge, EX-2 começa da branch padrão atualizada.
func TestConflictingJobWaitsForThePROfTheEarlierOne(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	ex2 := addFinding(t, e, "bbbbbbbbbbbb", "Trata erro do handler", []string{"cmd/api"}, "cmd/api/main.go")
	ex3 := addFinding(t, e, "cccccccccccc", "Fecha conexões do store", []string{"internal/store"}, "internal/store/store.go")
	for _, id := range []string{"EX-1", ex2, ex3} {
		e.lin.AddLabel(id, "sentinela-fix") // decisão humana
	}
	if q, err := e.w.Enqueue(ctx); err != nil || len(q) != 3 {
		t.Fatalf("enfileirados = %d (%v)", len(q), err)
	}
	byIssue := func(id string) db.Execution {
		rows, err := e.st.Q.ListExecutions(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.IssueIdentifier == id {
				return r
			}
		}
		t.Fatalf("sem job para %s", id)
		return db.Execution{}
	}

	if ran, err := e.w.RunNext(ctx); err != nil || !ran {
		t.Fatalf("o primeiro job deveria rodar: %v %v", ran, err)
	}
	first := byIssue("EX-1")
	if first.Status != "succeeded" || !first.PrNumber.Valid {
		t.Fatalf("EX-1 = %s, PR %v, erro %s", first.Status, first.PrNumber, first.Error.String)
	}
	if ran, err := e.w.RunNext(ctx); err != nil || !ran {
		t.Fatalf("o job sem conflito deveria rodar: %v %v", ran, err)
	}
	if j := byIssue(ex3); j.Status != "succeeded" {
		t.Fatalf("%s = %s, quer succeeded (sem sobreposição)", ex3, j.Status)
	}
	if ran, err := e.w.RunNext(ctx); err != nil || ran {
		t.Fatalf("com o PR da EX-1 aberto, nenhum job pode começar: %v %v", ran, err)
	}
	waiting := byIssue(ex2)
	want := "aguarda job " + strconv.FormatInt(first.ID, 10) + " (EX-1), PR #" + strconv.Itoa(int(first.PrNumber.Int32)) + " aberto: caminho em comum cmd/api"
	if waiting.Status != "queued" || !strings.Contains(waiting.WaitReason.String, want) {
		t.Fatalf("%s = %s, motivo %q; quer conter %q", ex2, waiting.Status, waiting.WaitReason.String, want)
	}
	if w, err := e.w.Waiting(ctx); err != nil || len(w) != 1 || w[0].IssueIdentifier != ex2 {
		t.Fatalf("Waiting = %+v (%v)", w, err)
	}

	// Uma pessoa mergeia o PR da EX-1 (simulado no remoto e no estado gravado pelo acompanhamento).
	gitIn(t, e.remote, "update-ref", "refs/heads/main", "refs/heads/sentinela/EX-1")
	if _, err := e.st.Q.SetPRState(ctx, db.SetPRStateParams{Repo: "acme/svc", PrNumber: first.PrNumber, PrState: text("merged")}); err != nil {
		t.Fatal(err)
	}
	if ran, err := e.w.RunNext(ctx); err != nil || !ran {
		t.Fatalf("depois do merge o job deveria rodar: %v %v", ran, err)
	}
	done := byIssue(ex2)
	if done.Status != "succeeded" || done.WaitReason.Valid {
		t.Fatalf("%s = %s, motivo %q", ex2, done.Status, done.WaitReason.String)
	}
	merged := gitIn(t, e.remote, "rev-parse", "main")
	if base := gitIn(t, done.Worktree.String, "merge-base", "HEAD", merged); base != merged {
		t.Errorf("o job deveria partir da main com o merge (%s), merge-base %s", merged, base)
	}
}
