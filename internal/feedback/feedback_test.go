package feedback

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pgvector/pgvector-go"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	url := os.Getenv("SENTINELA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SENTINELA_TEST_DATABASE_URL não definida")
	}
	ctx := context.Background()
	m, err := store.NewMigrator(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = m.DownTo(context.Background(), 0)
		_ = m.Close()
	})
	if _, err := m.DownTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}

func TestCuratorTurnsOutcomesIntoExamplesSuppressionsAndThresholds(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	cfg, err := config.Load(filepath.Join("..", "..", "testdata", "sentinela.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	const repoName = "acme/app-java-angular"
	id, err := st.Q.InsertFinding(ctx, db.InsertFindingParams{
		Fingerprint: "aaaaaaaaaaaa", Source: "code_analysis", Repo: repoName, Module: "backend/controller",
		Owners: []string{}, Routing: []byte("{}"), Category: "security", Severity: "high", Confidence: 0.7,
		Title: "Exige autorização no endpoint de exclusão de URL", Evidence: []byte(`[{"file":"backend/controller/C.java","line":72}]`),
		Signals: []byte("[]"), AcceptanceCriteria: []string{}, ScopePaths: []string{}, RelatedModules: []string{},
		Embedding: pgvector.NewVector(make([]float32, 1024)), JudgeConfidence: pgtype.Float4{Float32: 0.7, Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Q.InsertOutcome(ctx, db.InsertOutcomeParams{FindingID: id, Kind: "rejected", Reason: pgtype.Text{String: "O app não tem login; é decisão de produto.", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	// Vale quem decidiu (ADR 0022): decisão humana executada por agente entra na calibração;
	// decisão de agente (viola o princípio 6) fica fora.
	if _, err := st.Pool.Exec(ctx, `INSERT INTO outcomes (finding_id, kind, reason, decided_by, executed_by, note) VALUES
		($1, 'rejected', 'decidido pelo dono, executado no chat', 'human', 'agent', 'executada pelo Claude do chat'),
		($1, 'rejected', 'o agente decidiu sozinho', 'agent', 'agent', 'não deveria existir')`, id); err != nil {
		t.Fatal(err)
	}

	// Exemplo confirmado por humano: a curadoria não o desativa.
	if _, err := st.Pool.Exec(ctx, `INSERT INTO feedback_examples (repo, category, finding_id, outcome, title, lesson, confirmed_by, confirmed_at)
		VALUES ($1, 'bug', $2, 'rejected', 'Confirmado', 'Lição confirmada.', 'Fulano', now())`, repoName, id); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "curator"), 0o750)
	resp := `{"examples":[{"finding_id":` + itoa(id) + `,"lesson":"O time recusa exigir autenticação num app sem login."}],` +
		`"suppressions":[{"module":"backend/generated","category":"bug","pattern":"backend/generated/**","reason":"Código gerado."},{"module":"","category":"bug","pattern":"**","reason":"tudo"}],` +
		`"thresholds":[{"module":"backend/controller","category":"security","min_confidence":0.8,"reason":"Recusou segurança com 0,7."}]}`
	if err := os.WriteFile(filepath.Join(dir, "curator", "default.json"), []byte(resp), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cur := &Curator{Cfg: cfg, Store: st, Tracer: noop.NewTracerProvider().Tracer("t"), Now: func() time.Time { return now }}
	if due, _, err := cur.Due(ctx); err != nil || !due {
		t.Fatalf("curadoria nunca rodou e deveria estar vencida: %v %v", due, err)
	}
	client := &llm.Replay{Dir: dir, Recorder: store.NewLLMRecorder(st), B: llm.NewCallBudget(5)}
	res, err := cur.Curate(ctx, client, repoName, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcomes != 2 || len(res.Examples) != 1 || len(res.Suppressions) != 1 || len(res.Thresholds) != 1 || len(res.Notes) != 1 {
		t.Fatalf("curadoria = %+v", res)
	}
	if due, _, _ := cur.Due(ctx); due {
		t.Error("logo depois de curar, a curadoria não deveria estar vencida")
	}

	ex, err := JudgeExamples(ctx, st.Q, repoName, 6)
	if err != nil || len(ex) != 2 || ex[0].Reason != "Lição confirmada." || ex[1].Outcome != "recusado" || ex[1].Reason != "O time recusa exigir autenticação num app sem login." {
		t.Errorf("exemplos do juiz = %+v (%v)", ex, err)
	}
	th, err := LoadThresholds(ctx, st.Q, repoName, 0.6)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		module, category string
		want             float64
	}{{"backend/controller", "security", 0.8}, {"backend/controller/admin", "security", 0.8}, {"backend/controllers", "security", 0.6}, {"backend/controller", "bug", 0.6}} {
		if got, _ := th.For(c.module, c.category); float32(got) != float32(c.want) {
			t.Errorf("limiar %s/%s = %v, quer %v", c.module, c.category, got, c.want)
		}
	}
	var pattern, reason string
	var expires pgtype.Timestamptz
	if err := st.Pool.QueryRow(ctx, "SELECT pattern, reason, expires_at FROM suppressions").Scan(&pattern, &reason, &expires); err != nil ||
		pattern != "backend/generated/**" || reason != "curator: Código gerado." || expires.Time.Before(now.Add(80*24*time.Hour)) {
		t.Errorf("supressão = %q %q %v (%v)", pattern, reason, expires.Time, err)
	}

	// Sem desfechos novos na janela: nenhuma chamada.
	before := client.B.Calls()
	again, err := cur.Curate(ctx, client, repoName, now.Add(time.Minute))
	if err != nil || again.Skipped == "" || client.B.Calls() != before {
		t.Errorf("curadoria vazia = %+v (%v), chamadas %d→%d", again, err, before, client.B.Calls())
	}
}

func itoa(i int64) string { return strconv.FormatInt(i, 10) }

// Supressão de uma recusa decidida por agente não barra nada, e o achado não vira exemplo; a de
// uma recusa humana executada por agente vale (ADR 0022).
func TestSuppressionsAndExamplesFollowWhoDecided(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	add := func(fp string, decided, executed string) {
		id, err := st.Q.InsertFinding(ctx, db.InsertFindingParams{
			Fingerprint: fp, Source: "code_analysis", Repo: "r/x", Module: "m", Owners: []string{}, Routing: []byte("{}"),
			Category: "bug", Severity: "low", Confidence: 0.7, Title: "achado " + fp, Evidence: []byte("[]"), Signals: []byte("[]"),
			AcceptanceCriteria: []string{}, ScopePaths: []string{}, RelatedModules: []string{}, Embedding: pgvector.NewVector(make([]float32, 1024)),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.Pool.Exec(ctx, "UPDATE findings SET state = 'rejected' WHERE id = $1", id); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Pool.Exec(ctx, "INSERT INTO outcomes (finding_id, kind, reason, decided_by, executed_by) VALUES ($1, 'rejected', 'motivo', $2, $3)", id, decided, executed); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Pool.Exec(ctx, "INSERT INTO suppressions (repo, module, fingerprint, reason, finding_id) VALUES ('r/x', 'm', $1, 'motivo', $2)", fp, id); err != nil {
			t.Fatal(err)
		}
	}
	add("humano00exec", "human", "agent")
	add("agente000000", "agent", "agent")
	sups, err := st.Q.ActiveSuppressions(ctx, "r/x")
	if err != nil || len(sups) != 1 || sups[0].Fingerprint.String != "humano00exec" {
		t.Errorf("supressões ativas = %+v (%v)", sups, err)
	}
	ex, err := st.Q.JudgeExamples(ctx, db.JudgeExamplesParams{Repo: "r/x", Limit: 10})
	if err != nil || len(ex) != 1 || ex[0].Title != "achado humano00exec" {
		t.Errorf("exemplos = %+v (%v)", ex, err)
	}
	bad, err := st.Q.AgentDecisions(ctx)
	if err != nil || len(bad) != 1 {
		t.Errorf("decisões de agente = %+v (%v)", bad, err)
	}
}
