package local

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pgvector/pgvector-go"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

func setup(t *testing.T, tracker string) (*Tracker, *store.Store) {
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
	t.Cleanup(func() { _, _ = m.DownTo(context.Background(), 0); _ = m.Close() })
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
	base, err := os.ReadFile(filepath.Join("..", "..", "sentinela.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(append(base, []byte("\nadapters: { tracker: "+tracker+", notifier: none, git_host: gh_cli }\n")...))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Adapters.LocalDir = filepath.Join(t.TempDir(), "reports")
	return &Tracker{Cfg: cfg, Store: st, ConfigFlag: "--config eval.yaml"}, st
}

func addFinding(t *testing.T, st *store.Store, runID int64, fp, title string) int64 {
	t.Helper()
	routing, _ := json.Marshal(repo.Routing{Team: "platform", Rule: "repo"})
	id, err := st.Q.InsertFinding(context.Background(), db.InsertFindingParams{
		RunID: pgtype.Int8{Int64: runID, Valid: runID != 0}, Fingerprint: fp, Source: "code_analysis", Repo: "acme/monorepo", Module: "libs/http",
		Owners: []string{}, Routing: routing, Category: "bug", Severity: "medium", Confidence: 0.8, Title: title, Context: "Por que importa.",
		Evidence: []byte(`[{"file":"libs/http/server.go","line":12}]`), Signals: []byte("[]"), ProposedFix: "Usar http.Server com timeouts.",
		AcceptanceCriteria: []string{"Servidor com ReadHeaderTimeout"}, ScopePaths: []string{"libs/http"}, RelatedModules: []string{},
		Embedding: pgvector.NewVector(make([]float32, 1024)), JudgeConfidence: pgtype.Float4{Float32: 0.75, Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestDecisionsOnlyInLocalMode(t *testing.T) {
	tr, st := setup(t, config.TrackerLinearAPI)
	id := addFinding(t, st, 0, "aaaaaaaaaaaa", "Configura timeouts")
	ctx := context.Background()
	if err := tr.Accept(ctx, id, Decision{}); !errors.Is(err, ErrNotLocal) {
		t.Errorf("accept no modo linear_api: %v", err)
	}
	if _, err := tr.Reject(ctx, id, "não é bug", Decision{}); !errors.Is(err, ErrNotLocal) {
		t.Errorf("reject no modo linear_api: %v", err)
	}
	if _, err := tr.Fix(ctx, id, Decision{}); !errors.Is(err, ErrNotLocal) {
		t.Errorf("fix no modo linear_api: %v", err)
	}
}

// reject grava o mesmo que cancelar a issue no Linear: achado rejected, outcome humano com o
// motivo e supressão pelo fingerprint (que a próxima rodada respeita).
func TestRejectRecordsHumanDecisionAndSuppression(t *testing.T) {
	tr, st := setup(t, config.TrackerLocal)
	ctx := context.Background()
	id := addFinding(t, st, 0, "bbbbbbbbbbbb", "Valida entrada")
	if _, err := tr.Reject(ctx, id, "", Decision{}); err == nil {
		t.Error("recusa sem motivo aceita")
	}
	if _, err := tr.Reject(ctx, id, "x", Decision{ExecutedBy: ByAgent}); err == nil || !strings.Contains(err.Error(), "--note") {
		t.Errorf("agente sem nota: %v", err)
	}
	sup, err := tr.Reject(ctx, id, "o handler já valida antes", Decision{Actor: "dono", ExecutedBy: ByAgent, Note: "teste do modo local"})
	if err != nil || sup == 0 {
		t.Fatalf("reject: %d, %v", sup, err)
	}
	f, _ := st.Q.GetFinding(ctx, id)
	outs, _ := st.Q.FindingOutcomes(ctx, id)
	if f.State != "rejected" || f.ResolutionReason.String != "o handler já valida antes" {
		t.Errorf("achado = %s %q", f.State, f.ResolutionReason.String)
	}
	if len(outs) != 1 || outs[0].Kind != "rejected" || outs[0].DecidedBy != "human" || outs[0].ExecutedBy != "agent" || outs[0].Note.String != "teste do modo local" || outs[0].Actor.String != "dono" {
		t.Errorf("outcomes = %+v", outs)
	}
	sups, err := st.Q.ActiveSuppressions(ctx, "acme/monorepo")
	if err != nil || len(sups) != 1 || sups[0].Fingerprint.String != "bbbbbbbbbbbb" {
		t.Errorf("supressões = %+v, %v", sups, err)
	}
	if _, err := tr.Reject(ctx, id, "de novo", Decision{}); err == nil || !strings.Contains(err.Error(), "já está rejected") {
		t.Errorf("segunda recusa: %v", err)
	}
}

// fix é aplicar a label de gatilho: enfileira o job sem issue no Linear e registra a aceitação.
// Depois de uma falha sem PR, é nova tentativa; com job ativo ou PR aberto, recusa.
func TestFixEnqueuesAndAccepts(t *testing.T) {
	tr, st := setup(t, config.TrackerLocal)
	ctx := context.Background()
	id := addFinding(t, st, 0, "cccccccccccc", "Fecha o body")
	if err := tr.Accept(ctx, id, Decision{Actor: "dono"}); err != nil {
		t.Fatal(err)
	}
	if err := tr.Accept(ctx, id, Decision{}); err == nil {
		t.Error("aceitou duas vezes")
	}
	e, err := tr.Fix(ctx, id, Decision{Actor: "dono"})
	if err != nil {
		t.Fatal(err)
	}
	if e.IssueIdentifier != "F-"+strconv.FormatInt(id, 10) || e.LinearIssueID != "" || e.Kind != "fix" || e.Status != "queued" || e.PreviousID.Valid {
		t.Errorf("execução = %+v", e)
	}
	if outs, _ := st.Q.FindingOutcomes(ctx, id); len(outs) != 1 {
		t.Errorf("fix depois do accept não deveria aceitar de novo: %+v", outs)
	}
	if _, err := tr.Fix(ctx, id, Decision{}); err == nil || !strings.Contains(err.Error(), "queued") {
		t.Errorf("fix com job na fila: %v", err)
	}
	if _, err := st.Pool.Exec(ctx, "UPDATE executions SET status = 'failed', decision = 'failed' WHERE id = $1", e.ID); err != nil {
		t.Fatal(err)
	}
	again, err := tr.Fix(ctx, id, Decision{})
	if err != nil || again.PreviousID.Int64 != e.ID {
		t.Fatalf("nova tentativa: %+v, %v", again, err)
	}
	// Achado já aceito com job falho: o fix só reenfileira, sem nova decisão (idempotente).
	if outs, _ := st.Q.FindingOutcomes(ctx, id); len(outs) != 1 {
		t.Errorf("a nova tentativa gravou outra decisão: %+v", outs)
	}
	if _, err := st.Pool.Exec(ctx, "UPDATE executions SET status = 'succeeded', pr_number = 7, pr_url = 'https://github.com/acme/monorepo/pull/7', pr_state = 'open' WHERE id = $1", again.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Fix(ctx, id, Decision{}); err == nil || !strings.Contains(err.Error(), "job rework") {
		t.Errorf("fix com PR aberto: %v", err)
	}

	// fix sem accept registra a aceitação junto.
	other := addFinding(t, st, 0, "dddddddddddd", "Outro")
	if _, err := tr.Fix(ctx, other, Decision{Actor: "dono", ExecutedBy: ByAgent, Note: "teste do modo local"}); err != nil {
		t.Fatal(err)
	}
	outs, _ := st.Q.FindingOutcomes(ctx, other)
	if len(outs) != 1 || outs[0].Kind != "accepted" || outs[0].ExecutedBy != "agent" || outs[0].Note.String != "teste do modo local" {
		t.Errorf("outcomes do fix = %+v", outs)
	}
}

// O relatório mostra o id e o comando de cada ação, separa abertos, em correção e decididos, e o
// Publish do fim da rodada lista os achados abertos daquela rodada.
func TestReportShowsIDsAndCommandsAndPublishListsTheRun(t *testing.T) {
	tr, st := setup(t, config.TrackerLocal)
	ctx := context.Background()
	run, err := st.Q.CreateRun(ctx, db.CreateRunParams{Repo: "acme/monorepo", BudgetUsd: store.NanoUSDToNumeric(0), Mode: "full"})
	if err != nil {
		t.Fatal(err)
	}
	open := addFinding(t, st, run.ID, "eeeeeeeeeeee", "Configura timeouts no servidor")
	rejected := addFinding(t, st, run.ID, "ffffffffffff", "Recusado")
	queued := addFinding(t, st, 0, "111111111111", "Na fila")
	if _, err := tr.Reject(ctx, rejected, "falso positivo", Decision{}); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Fix(ctx, queued, Decision{}); err != nil {
		t.Fatal(err)
	}
	res, err := tr.Publish(ctx, "acme/monorepo", run.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Issues) != 1 || res.Issues[0].FindingID != open || res.Issues[0].Identifier != "F-"+strconv.FormatInt(open, 10) || !strings.HasSuffix(res.Issues[0].URL, "acme__monorepo.md#f-"+strconv.FormatInt(open, 10)) {
		t.Errorf("publicadas = %+v", res.Issues)
	}
	md, err := os.ReadFile(filepath.Join(tr.Cfg.Adapters.LocalDir, "acme__monorepo.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(md)
	o := strconv.FormatInt(open, 10)
	for _, want := range []string{
		"# Sentinela · acme/monorepo", "## Abertos (1)", "## Em correção (1)", "## Decididos (1)",
		"### F-" + o + " · Configura timeouts no servidor", "`libs/http/server.go:12`", "Judge 0.75",
		"sentinela --config eval.yaml finding accept " + o, `sentinela --config eval.yaml finding reject ` + o + ` "<motivo>"`,
		"sentinela --config eval.yaml finding fix " + o, "rejeitado: falso positivo", "job 1 queued",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("relatório sem %q:\n%s", want, s)
		}
	}
	h, err := os.ReadFile(filepath.Join(tr.Cfg.Adapters.LocalDir, "acme__monorepo.html"))
	if err != nil || !strings.Contains(string(h), `id="f-`+o+`"`) || !strings.Contains(string(h), "finding fix "+o) {
		t.Errorf("html: %v", err)
	}
	idx, err := os.ReadFile(filepath.Join(tr.Cfg.Adapters.LocalDir, "index.md"))
	if err != nil || !strings.Contains(string(idx), "| acme/monorepo | 1 | 3 |") {
		t.Errorf("índice: %s %v", idx, err)
	}
}

// PR em draft com ESCALATE: o relatório aponta job approve e job rework, sem fix.
func TestReportPointsToJobCommandsWhenThePRWaitsForAHuman(t *testing.T) {
	tr, st := setup(t, config.TrackerLocal)
	ctx := context.Background()
	id := addFinding(t, st, 0, "222222222222", "Atualiza Go")
	e, err := tr.Fix(ctx, id, Decision{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `UPDATE executions SET status = 'succeeded', decision = 'escalate', pr_number = 2,
		pr_url = 'https://github.com/acme/monorepo/pull/2', pr_state = 'open' WHERE id = $1`, e.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, "UPDATE findings SET state = 'executing' WHERE id = $1", id); err != nil {
		t.Fatal(err)
	}
	p, err := tr.Build(ctx, "acme/monorepo")
	if err != nil {
		t.Fatal(err)
	}
	it := p.Sections[1].Items[0]
	cmds := strings.Join(it.Commands, "\n")
	ex := strconv.FormatInt(e.ID, 10)
	if !strings.Contains(cmds, "job approve "+ex) || !strings.Contains(cmds, "job rework "+ex) || strings.Contains(cmds, "finding fix") {
		t.Errorf("comandos = %v", it.Commands)
	}
}

// resolve: a correção foi feita à mão; o achado fecha como fixed com o outcome accepted e a nota.
func TestResolveClosesTheFindingAsFixed(t *testing.T) {
	tr, st := setup(t, config.TrackerLocal)
	ctx := context.Background()
	id := addFinding(t, st, 0, "333333333333", "Fecha o body")
	if err := tr.Resolve(ctx, id, Decision{}); err == nil || !strings.Contains(err.Error(), "--note") {
		t.Errorf("sem nota: %v", err)
	}
	e, err := tr.Fix(ctx, id, Decision{})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Resolve(ctx, id, Decision{Note: "x"}); err == nil || !strings.Contains(err.Error(), "job cancel") {
		t.Errorf("com job na fila: %v", err)
	}
	if _, err := st.Pool.Exec(ctx, "UPDATE executions SET status = 'canceled' WHERE id = $1", e.ID); err != nil {
		t.Fatal(err)
	}
	if err := tr.Resolve(ctx, id, Decision{Actor: "dono", Note: "corrigido no commit abc123"}); err != nil {
		t.Fatal(err)
	}
	f, _ := st.Q.GetFinding(ctx, id)
	outs, _ := st.Q.FindingOutcomes(ctx, id)
	last := outs[len(outs)-1]
	if f.State != "fixed" || !strings.Contains(f.ResolutionReason.String, "corrigido no commit abc123") {
		t.Errorf("achado = %s %q", f.State, f.ResolutionReason.String)
	}
	if last.Kind != "accepted" || last.DecidedBy != "human" || last.ExecutedBy != "human" || last.Note.String != "corrigido no commit abc123" {
		t.Errorf("outcome = %+v", last)
	}
	if err := tr.Resolve(ctx, id, Decision{Note: "de novo"}); err == nil || !strings.Contains(err.Error(), "já está fixed") {
		t.Errorf("segunda resolução: %v", err)
	}
}

// duplicate: o mais antigo absorve o mais novo (mesmo com a ordem trocada), sem supressão, com o
// outcome duplicate_of decidido por humano (ADR 0045).
func TestDuplicateMergesTheNewerIntoTheOlderWithoutSuppression(t *testing.T) {
	tr, st := setup(t, config.TrackerLocal)
	ctx := context.Background()
	older := addFinding(t, st, 0, "444444444444", "Antigo")
	newer := addFinding(t, st, 0, "555555555555", "Novo")
	if _, _, err := tr.Duplicate(ctx, older, older, Decision{}); err == nil {
		t.Error("duplicata de si mesmo aceita")
	}
	dup, orig, err := tr.Duplicate(ctx, older, newer, Decision{Actor: "dono"}) // ordem trocada
	if err != nil || dup != newer || orig != older {
		t.Fatalf("duplicata %d de %d, %v (quer o mais novo absorvido pelo mais antigo)", dup, orig, err)
	}
	f, _ := st.Q.GetFinding(ctx, newer)
	outs, _ := st.Q.FindingOutcomes(ctx, newer)
	sups, _ := st.Q.ActiveSuppressions(ctx, "acme/monorepo")
	if f.MergedInto.Int64 != older || f.State != "open" || len(outs) != 1 || outs[0].Kind != "duplicate_of" || outs[0].DecidedBy != "human" || len(sups) != 0 {
		t.Errorf("achado %+v / outcomes %+v / supressões %d", f.MergedInto, outs, len(sups))
	}
	if _, _, err := tr.Duplicate(ctx, newer, older, Decision{}); err == nil || !strings.Contains(err.Error(), "mesclado") {
		t.Errorf("duplicata já mesclada: %v", err)
	}
}

// answer: intentional recusa com supressão; not-intentional vira achado aberto e aceito; o
// relatório mostra os comandos enquanto a pergunta está aberta (ADR 0045).
func TestAnswerToTheProductQuestion(t *testing.T) {
	tr, st := setup(t, config.TrackerLocal)
	ctx := context.Background()
	plain := addFinding(t, st, 0, "666666666666", "Sem pergunta")
	a := addFinding(t, st, 0, "777777777777", "Pergunta A")
	b := addFinding(t, st, 0, "888888888888", "Pergunta B")
	if _, err := st.Pool.Exec(ctx, "UPDATE findings SET premise = 'O limite de 3 tentativas é de propósito?' WHERE id IN ($1, $2)", a, b); err != nil {
		t.Fatal(err)
	}
	if err := tr.Answer(ctx, plain, AnswerIntentional, Decision{}); err == nil || !strings.Contains(err.Error(), "não há pergunta") {
		t.Errorf("sem premissa: %v", err)
	}
	p, err := tr.Build(ctx, "acme/monorepo")
	if err != nil {
		t.Fatal(err)
	}
	var cmds string
	for _, it := range p.Sections[0].Items {
		if it.ID == a {
			cmds = strings.Join(it.Commands, "\n")
		}
	}
	if !strings.Contains(cmds, "finding answer "+strconv.FormatInt(a, 10)+" intentional") || !strings.Contains(cmds, "finding duplicate "+strconv.FormatInt(a, 10)+" --of F-<outro>") {
		t.Errorf("comandos do relatório: %s", cmds)
	}
	if err := tr.Answer(ctx, a, AnswerIntentional, Decision{Note: "regra do produto"}); err != nil {
		t.Fatal(err)
	}
	fa, _ := st.Q.GetFinding(ctx, a)
	if fa.State != "rejected" || fa.PremiseAnswer.String != "intentional" || !strings.Contains(fa.ResolutionReason.String, "É intencional: regra do produto") {
		t.Errorf("intencional: %s %s %q", fa.State, fa.PremiseAnswer.String, fa.ResolutionReason.String)
	}
	if err := tr.Answer(ctx, b, AnswerNotIntentional, Decision{Actor: "dono"}); err != nil {
		t.Fatal(err)
	}
	fb, _ := st.Q.GetFinding(ctx, b)
	outs, _ := st.Q.FindingOutcomes(ctx, b)
	if fb.State != "open" || fb.PremiseAnswer.String != "not_intentional" || len(outs) != 1 || outs[0].Kind != "accepted" || outs[0].DecidedBy != "human" {
		t.Errorf("não intencional: %s %s %+v", fb.State, fb.PremiseAnswer.String, outs)
	}
	if err := tr.Answer(ctx, b, AnswerIntentional, Decision{}); err == nil || !strings.Contains(err.Error(), "já foi respondida") {
		t.Errorf("segunda resposta: %v", err)
	}
}

func TestDuplicateAndAnswerOnlyInLocalMode(t *testing.T) {
	tr, st := setup(t, config.TrackerLinearAPI)
	ctx := context.Background()
	a := addFinding(t, st, 0, "999999999999", "A")
	b := addFinding(t, st, 0, "aaaa99999999", "B")
	if _, _, err := tr.Duplicate(ctx, a, b, Decision{}); !errors.Is(err, ErrNotLocal) {
		t.Errorf("duplicate fora do local: %v", err)
	}
	if err := tr.Answer(ctx, a, AnswerIntentional, Decision{}); !errors.Is(err, ErrNotLocal) {
		t.Errorf("answer fora do local: %v", err)
	}
}
