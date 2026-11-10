package discovery

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/linear/lineartest"
	"github.com/dev-danilocordeiro/sentinela/internal/notify"
)

// fakeSlack grava as mensagens por canal e falha nos canais de fail.
type fakeSlack struct {
	mu    sync.Mutex
	posts map[string][]string
	fail  map[string]error
}

func (f *fakeSlack) Post(_ context.Context, channel, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail[channel]; err != nil {
		return err
	}
	if f.posts == nil {
		f.posts = map[string][]string{}
	}
	f.posts[channel] = append(f.posts[channel], text)
	return nil
}

// Fase 3, ponta a ponta com Linear e Slack falsos: a rodada publica o achado aprovado,
// o time é avisado no próprio canal com menção, a issue é cancelada com um comentário, o
// polling grava rejected e a supressão com o motivo, e a rodada seguinte não traz o achado
// de volta nem cria issue nova.
func TestRejectedFindingDoesNotComeBackInTheNextRun(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	fl := lineartest.New()
	srv := httptest.NewServer(fl)
	t.Cleanup(srv.Close)
	client := linear.NewClient(srv.URL, "k")
	slack := &fakeSlack{}
	e.runner.Cfg.Publish.Enabled = true
	e.runner.Publisher = &linear.Publisher{Cfg: e.runner.Cfg, Store: e.st, Linear: client, Notify: &notify.Router{Notifier: slack, Cfg: e.runner.Cfg}}

	first, err := e.runner.Run(ctx, Options{Repo: "acme/svc"})
	if err != nil || first.PublishErr != nil {
		t.Fatalf("primeira rodada: %v / %v", err, first.PublishErr)
	}
	issues := fl.Snapshot()
	if len(issues) != 1 || first.Stats.Issues == nil || len(first.Stats.Issues.Issues) != 1 {
		t.Fatalf("issues = %d, stats = %+v", len(issues), first.Stats.Issues)
	}
	is := issues[0]
	if is.ProjectID != "proj-core" || is.StateID != "st-backlog" || len(is.LabelIDs) != 1 || is.LabelIDs[0] != "lbl-sentinela" || is.Priority != 2 {
		t.Errorf("issue mal roteada: %+v", is)
	}
	if !strings.Contains(is.Description, "sentinela-fingerprint: ") || !strings.Contains(is.Description, "/blob/") {
		t.Errorf("corpo sem fingerprint ou permalink:\n%s", is.Description)
	}
	if msgs := slack.posts["C0CORETEAM1"]; len(msgs) != 1 || !strings.HasPrefix(msgs[0], "<!subteam^S0COREGRP1> Nova issue") || !strings.Contains(msgs[0], is.Identifier) {
		t.Errorf("aviso do time = %v", slack.posts)
	}

	// Rodada completa repetida: o achado já existe; nada novo no Linear.
	again, err := e.runner.Run(ctx, Options{Repo: "acme/svc", Full: true})
	if err != nil || len(again.Stats.Issues.Issues) != 0 || len(fl.Snapshot()) != 1 || again.Stats.Duplicates != 1 {
		t.Fatalf("rodada repetida: err=%v issues=%+v linear=%d", err, again.Stats.Issues, len(fl.Snapshot()))
	}

	// Uma pessoa cancela a issue e comenta o motivo; o polling traz a decisão.
	const reason = "Falso positivo: Save é idempotente e o erro é tratado no chamador."
	fl.Cancel(is.Identifier, "Fulano", reason)
	syncer := &linear.Syncer{Store: e.st, Linear: client}
	res, err := syncer.Poll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changes) != 1 || res.Changes[0].Outcome != "rejected" || res.Changes[0].Reason != reason || res.Changes[0].Actor != "Fulano" || res.Changes[0].SuppressionID == 0 {
		t.Fatalf("sync = %+v", res)
	}
	var state, resolution string
	if err := e.st.Pool.QueryRow(ctx, "SELECT state, resolution_reason FROM findings").Scan(&state, &resolution); err != nil || state != "rejected" || resolution != reason {
		t.Errorf("achado = %s / %q (%v)", state, resolution, err)
	}
	var supReason, supFP string
	if err := e.st.Pool.QueryRow(ctx, "SELECT reason, fingerprint FROM suppressions WHERE id = $1", res.Changes[0].SuppressionID).Scan(&supReason, &supFP); err != nil || supReason != reason || supFP == "" {
		t.Errorf("supressão = %q %q (%v)", supReason, supFP, err)
	}
	var outcome string
	if err := e.st.Pool.QueryRow(ctx, "SELECT kind FROM outcomes").Scan(&outcome); err != nil || outcome != "rejected" {
		t.Errorf("outcome = %q (%v)", outcome, err)
	}

	// Mesma versão da issue no próximo ciclo: deduplicada por event_id, sem mudança.
	res2, err := syncer.Poll(ctx)
	if err != nil || len(res2.Changes) != 0 {
		t.Errorf("segundo polling = %+v (%v)", res2, err)
	}

	// Próxima rodada completa: o modelo propõe o mesmo problema, a supressão barra e o
	// Linear continua com uma issue só.
	creates := fl.Count("SentinelaCreateIssue")
	third, err := e.runner.Run(ctx, Options{Repo: "acme/svc", Full: true})
	if err != nil {
		t.Fatal(err)
	}
	if third.Stats.Suppressed != 1 || third.Stats.Published != 0 || len(third.Stats.Issues.Issues) != 0 || fl.Count("SentinelaCreateIssue") != creates || e.findingCount(t) != 1 {
		t.Errorf("rodada depois da recusa: %+v, issues criadas %d → %d", third.Stats, creates, fl.Count("SentinelaCreateIssue"))
	}
}

// Canal do time com erro (bot fora do canal): a rodada segue, o aviso fica nas estatísticas
// e o canal padrão recebe a explicação.
func TestTeamChannelFailureFallsBackToDefaultChannel(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	fl := lineartest.New()
	srv := httptest.NewServer(fl)
	t.Cleanup(srv.Close)
	slack := &fakeSlack{fail: map[string]error{"C0CORETEAM1": errors.New("slack chat.postMessage: not_in_channel")}}
	e.runner.Publisher = &linear.Publisher{Cfg: e.runner.Cfg, Store: e.st, Linear: linear.NewClient(srv.URL, "k"), Notify: &notify.Router{Notifier: slack, Cfg: e.runner.Cfg}}
	res, err := e.runner.Run(ctx, Options{Repo: "acme/svc"})
	if err != nil || res.Status != "succeeded" || len(fl.Snapshot()) != 1 {
		t.Fatalf("rodada = %s, %v", res.Status, err)
	}
	if w := res.Stats.Issues.Warnings; len(w) != 1 || !strings.Contains(w[0], "not_in_channel") {
		t.Errorf("avisos = %v", w)
	}
	if msgs := slack.posts["C0DEFAULT01"]; len(msgs) != 1 || !strings.Contains(msgs[0], "C0CORETEAM1") || !strings.Contains(msgs[0], "not_in_channel") {
		t.Errorf("canal padrão = %v", slack.posts)
	}
}

// Achado descartado pelo Judge não vai para findings; a rodada seguinte precisa lembrar
// dele (run_items) para não reapresentá-lo e arriscar uma decisão diferente.
func TestDiscardedFindingIsNotJudgedAgainInTheNextRun(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.fake.discard.Store(true)
	first, err := e.runner.Run(ctx, Options{Repo: "acme/svc"})
	if err != nil || first.Stats.Discarded != 1 || e.findingCount(t) != 0 {
		t.Fatalf("primeira rodada: %+v (%v)", first.Stats, err)
	}
	e.fake.discard.Store(false) // se o Judge fosse chamado de novo, publicaria
	second, err := e.runner.Run(ctx, Options{Repo: "acme/svc", Full: true})
	if err != nil {
		t.Fatal(err)
	}
	if second.Stats.Duplicates != 1 || second.Stats.Published != 0 || e.findingCount(t) != 0 {
		t.Errorf("segunda rodada: %+v", second.Stats)
	}
	items, _ := e.st.Q.ListRunItems(ctx, second.RunID)
	var remembered bool
	for _, it := range items {
		if it.Agent == "judge" && it.Status == "skipped" && strings.Contains(it.Reason.String, "já decidiu discard") {
			remembered = true
		}
	}
	if !remembered {
		t.Error("o motivo do pulo deveria citar a decisão anterior")
	}
}

// Sobreposição parcial (ADR 0016), como nas EX-5 e EX-7: o achado novo repete um critério de
// um achado já publicado. O código mostra a sobreposição ao Judge, o Judge reduz o escopo ao que
// sobra, e a issue nova sai só com o que sobra, linkada à existente. Se não sobra nada, é
// duplicado e nenhuma issue é criada.
func TestPartialOverlapNarrowsTheNewIssueAndLinksTheExistingOne(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	fl := lineartest.New()
	srv := httptest.NewServer(fl)
	t.Cleanup(srv.Close)
	e.runner.Publisher = &linear.Publisher{Cfg: e.runner.Cfg, Store: e.st, Linear: linear.NewClient(srv.URL, "k")}

	if _, err := e.runner.Run(ctx, Options{Repo: "acme/svc"}); err != nil || len(fl.Snapshot()) != 1 {
		t.Fatalf("primeira rodada: %v, issues %d", err, len(fl.Snapshot()))
	}
	existing := fl.Snapshot()[0]

	t.Run("não sobra nada: duplicado", func(t *testing.T) {
		e.fake.overlap.Store(2)
		res, err := e.runner.Run(ctx, Options{Repo: "acme/svc", Full: true})
		if err != nil || res.Stats.Published != 0 || res.Stats.Merged != 1 || len(fl.Snapshot()) != 1 {
			t.Fatalf("rodada = %+v (%v), issues %d", res.Stats, err, len(fl.Snapshot()))
		}
	})

	t.Run("sobra uma parte: escopo reduzido e issue relacionada", func(t *testing.T) {
		e.fake.overlap.Store(1)
		// A decisão anterior (merge) do mesmo fingerprint pularia o Judge; aqui o achado é outro.
		if _, err := e.st.Pool.Exec(ctx, "DELETE FROM run_items WHERE agent = 'judge' AND output->>'decision' = 'merge'"); err != nil {
			t.Fatal(err)
		}
		res, err := e.runner.Run(ctx, Options{Repo: "acme/svc", Full: true})
		if err != nil || res.Stats.Published != 1 || res.Stats.Narrowed != 1 {
			t.Fatalf("rodada = %+v (%v)", res.Stats, err)
		}
		issues := fl.Snapshot()
		if len(issues) != 2 {
			t.Fatalf("issues = %d", len(issues))
		}
		is := issues[1]
		if is.Title != "Trata erro ignorado ao apagar" || strings.Contains(is.Description, "Save com falha") || !strings.Contains(is.Description, "Delete com falha devolve erro ao chamador") {
			t.Errorf("issue nova deveria ter só o que sobra:\n%s\n%s", is.Title, is.Description)
		}
		if !strings.Contains(is.Description, "## Fora do escopo") || !strings.Contains(is.Description, existing.Identifier) {
			t.Errorf("corpo deveria apontar a issue existente:\n%s", is.Description)
		}
		if len(is.Related) != 1 || is.Related[0] != existing.ID {
			t.Errorf("relações = %v, quer [%s]", is.Related, existing.ID)
		}
		if got := res.Stats.Issues.Issues; len(got) != 1 || len(got[0].Overlaps) != 1 || got[0].Overlaps[0] != existing.Identifier {
			t.Errorf("resultado da publicação = %+v", got)
		}
		var criteria []string
		if err := e.st.Pool.QueryRow(ctx, "SELECT acceptance_criteria FROM findings WHERE title = 'Trata erro ignorado ao apagar'").Scan(&criteria); err != nil || len(criteria) != 1 {
			t.Errorf("critérios gravados = %v (%v)", criteria, err)
		}
		// A issue existente não muda.
		if fl.Snapshot()[0].Description != existing.Description || fl.Snapshot()[0].Title != existing.Title {
			t.Error("a issue existente não pode ser alterada")
		}
	})
}
