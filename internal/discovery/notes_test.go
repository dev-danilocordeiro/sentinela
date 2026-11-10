package discovery

import (
	"context"
	"strings"
	"testing"
)

// Notas fora do escopo dos reviewers viram achados candidatos (ADR 0019): em modo sombra só
// registram a decisão do Judge; publicadas, entram em findings com source review_note, ligadas
// à execução de origem, e a segunda vez é duplicado.
func TestReviewNotesBecomeCandidateFindingsThroughTheJudge(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.runner.Run(ctx, Options{Repo: "acme/svc"}); err != nil {
		t.Fatal(err)
	}
	var execID int64
	if err := e.st.Pool.QueryRow(ctx, `INSERT INTO executions (finding_id, status, repo, issue_identifier, pr_url, decision)
		SELECT id, 'succeeded', repo, 'EX-1', 'https://github.com/acme/svc/pull/1', 'approve' FROM findings LIMIT 1 RETURNING id`).Scan(&execID); err != nil {
		t.Fatal(err)
	}
	for _, rv := range []struct{ lens, items string }{
		{"security", `["(não bloqueia) internal/store/store.go:5: o erro de Delete também é descartado", "Verificado: sem segredos nem dependências novas"]`},
		{"reliability", `["(fora do escopo) internal/store/store.go:999: algo que não existe"]`},
		{"code", `[]`},
	} {
		if _, err := e.st.Pool.Exec(ctx, "INSERT INTO reviews (execution_id, round, lens, verdict, findings) VALUES ($1, 1, $2, 'PASS', $3)", execID, rv.lens, rv.items); err != nil {
			t.Fatal(err)
		}
	}
	before := e.findingCount(t)

	shadow, err := e.runner.RunReviewNotes(ctx, NotesOptions{ExecutionID: execID, Shadow: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(shadow.Notes) != 2 || len(shadow.Ignored) != 1 {
		t.Fatalf("notas = %+v, ignorados = %v", shadow.Notes, shadow.Ignored)
	}
	in, _ := e.fake.notesInput.Load().(string)
	if !strings.Contains(in, `"file":"internal/store/store.go","start_line":1`) || !strings.Contains(in, "_ = db.Delete(x)") {
		t.Errorf("o intérprete deveria receber o trecho real do arquivo citado: %s", in)
	}
	got := map[string]string{}
	for _, d := range shadow.Decisions {
		got[d.Title] = d.Decision
	}
	if got["Trata erro ignorado ao apagar"] != "publish" || got["Problema inventado"] != "skipped" {
		t.Fatalf("decisões = %+v", shadow.Decisions)
	}
	if e.findingCount(t) != before {
		t.Fatal("modo sombra não grava achado")
	}
	if len(shadow.Dropped) != 0 {
		t.Errorf("as duas notas viraram achado; nenhuma deveria aparecer como descartada: %+v", shadow.Dropped)
	}
	var mode string
	_ = e.st.Pool.QueryRow(ctx, "SELECT mode FROM analysis_runs WHERE id = $1", shadow.RunID).Scan(&mode)
	if mode != "review_note" {
		t.Errorf("modo da rodada = %q", mode)
	}
	if last, err := e.st.Q.LastCompletedRun(ctx, "acme/svc"); err != nil || last.ID == shadow.RunID {
		t.Error("rodada de notas não pode virar base do diff incremental")
	}

	pub, err := e.runner.RunReviewNotes(ctx, NotesOptions{ExecutionID: execID})
	if err != nil {
		t.Fatal(err)
	}
	var source string
	var origin int64
	var signals string
	if err := e.st.Pool.QueryRow(ctx, "SELECT source, origin_execution_id, signals::text FROM findings WHERE title = 'Trata erro ignorado ao apagar'").Scan(&source, &origin, &signals); err != nil {
		t.Fatalf("achado publicado não gravado: %v (%+v)", err, pub.Decisions)
	}
	if source != "review_note" || origin != execID || !strings.Contains(signals, `"kind": "review_note"`) || !strings.Contains(signals, "o erro de Delete também é descartado") {
		t.Errorf("achado = %s, origem %d, sinais %s", source, origin, signals)
	}

	again, err := e.runner.RunReviewNotes(ctx, NotesOptions{ExecutionID: execID})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range again.Decisions {
		if d.Decision == "publish" {
			t.Errorf("a mesma nota não pode publicar de novo: %+v", d)
		}
	}
	if e.findingCount(t) != before+1 {
		t.Errorf("achados = %d, quer %d", e.findingCount(t), before+1)
	}
}
