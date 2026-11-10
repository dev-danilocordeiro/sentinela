package linear_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dev-danilocordeiro/sentinela/internal/linear"
)

// flaky é um Tracker cujo Closure falha nas primeiras n chamadas (erro transitório do Linear).
type flaky struct {
	linear.Tracker
	fails int
}

func (f *flaky) Closure(ctx context.Context, id string) (linear.Closure, error) {
	if f.fails > 0 {
		f.fails--
		return linear.Closure{}, errors.New("linear: HTTP 502")
	}
	return f.Tracker.Closure(ctx, id)
}

func counts(t *testing.T, e *env) (state string, outcomes, sups int) {
	t.Helper()
	ctx := context.Background()
	_ = e.st.Pool.QueryRow(ctx, "SELECT state FROM findings").Scan(&state)
	_ = e.st.Pool.QueryRow(ctx, "SELECT count(*) FROM outcomes").Scan(&outcomes)
	_ = e.st.Pool.QueryRow(ctx, "SELECT count(*) FROM suppressions").Scan(&sups)
	return
}

// C1 (revisão 2026-10-11): o Linear falha no meio da aplicação de um cancelamento. O evento não
// pode ficar marcado como visto: o próximo poll da mesma versão da issue aplica a decisão, uma
// vez só (um outcome, uma supressão), e polls seguintes não aplicam de novo.
func TestPollRetriesADecisionWhenTheLinearFailsMidway(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.addFinding(t, "retrypoll001", "libs/r/a.go", "high")
	pub, err := e.pub.Publish(ctx, "acme/mono", 0, e.res)
	if err != nil {
		t.Fatal(err)
	}
	e.fake.Cancel(pub.Issues[0].Identifier, "Ana", "Não se aplica aqui.")
	syncer := &linear.Syncer{Store: e.st, Linear: &flaky{Tracker: e.cl, fails: 1}, Cfg: e.cfg}
	if _, err := syncer.Poll(ctx); err == nil {
		t.Fatal("o primeiro poll deveria falhar com o erro do Linear")
	}
	if st, o, s := counts(t, e); st != "open" || o != 0 || s != 0 {
		t.Fatalf("depois da falha: %s, %d outcome(s), %d supressão(ões)", st, o, s)
	}
	for range 3 {
		if _, err := syncer.Poll(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if st, o, s := counts(t, e); st != "rejected" || o != 1 || s != 1 {
		t.Errorf("depois de reprocessar: %s, %d outcome(s), %d supressão(ões) (quer rejected, 1, 1)", st, o, s)
	}
}

// O mesmo pelo webhook: a entrega que falhou é reprocessada na reentrega do Linear (mesmo
// Linear-Delivery), sem gravar nada em dobro.
func TestWebhookRedeliveryRetriesAFailedDecision(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.addFinding(t, "retryhook001", "libs/r/b.go", "high")
	pub, err := e.pub.Publish(ctx, "acme/mono", 0, e.res)
	if err != nil {
		t.Fatal(err)
	}
	e.fake.Cancel(pub.Issues[0].Identifier, "Ana", "Não se aplica aqui.")
	issueID := e.fake.Snapshot()[0].ID
	now := time.Now()
	h := &linear.WebhookHandler{Secret: "s3cr3t", Syncer: &linear.Syncer{Store: e.st, Linear: &flaky{Tracker: e.cl, fails: 1}}, Now: func() time.Time { return now }}
	body := []byte(fmt.Sprintf(`{"action":"update","type":"Issue","data":{"id":%q},"webhookTimestamp":%d}`, issueID, now.UnixMilli()))
	send := func() int {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/linear", bytes.NewReader(body))
		req.Header.Set("Linear-Signature", sign("s3cr3t", body))
		req.Header.Set("Linear-Delivery", "entrega-1")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := send(); code == http.StatusOK {
		t.Fatal("a entrega com falha do Linear não pode responder 200")
	}
	for range 2 {
		if code := send(); code != http.StatusOK {
			t.Fatalf("reentrega = %d", code)
		}
	}
	if st, o, s := counts(t, e); st != "rejected" || o != 1 || s != 1 {
		t.Errorf("depois da reentrega: %s, %d outcome(s), %d supressão(ões) (quer rejected, 1, 1)", st, o, s)
	}
}

// flakyUpdate falha no primeiro UpdateIssue (o Linear cai depois de a resposta ser gravada).
type flakyUpdate struct {
	linear.Tracker
	fails int
}

func (f *flakyUpdate) UpdateIssue(ctx context.Context, id, title, body string, add, remove []string) error {
	if f.fails > 0 {
		f.fails--
		return errors.New("linear: HTTP 502")
	}
	return f.Tracker.UpdateIssue(ctx, id, title, body, add, remove)
}

// Resposta "não é intencional" com o Linear falhando ao atualizar a issue: o poll seguinte termina
// a atualização, e o outcome accepted continua um só.
func TestNotIntentionalAnswerFinishesAfterALinearFailure(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.cfg.Labels.Question, e.cfg.Labels.Intentional, e.cfg.Labels.NotIntentional = "sentinela-pergunta", "sentinela-intencional", "sentinela-nao-intencional"
	id := e.addFinding(t, "retrypremis1", "libs/r/c.go", "medium")
	if _, err := e.st.Pool.Exec(ctx, "UPDATE findings SET premise = 'É público de propósito?' WHERE id = $1", id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pub.Publish(ctx, "acme/mono", 0, e.res); err != nil {
		t.Fatal(err)
	}
	is := e.fake.Snapshot()[0]
	e.fake.AddLabel(is.Identifier, "sentinela-nao-intencional")
	syncer := &linear.Syncer{Store: e.st, Linear: &flakyUpdate{Tracker: e.cl, fails: 1}, Cfg: e.cfg}
	if _, err := syncer.Poll(ctx); err == nil {
		t.Fatal("o primeiro poll deveria falhar")
	}
	for range 2 {
		if _, err := syncer.Poll(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var accepted int
	_ = e.st.Pool.QueryRow(ctx, "SELECT count(*) FROM outcomes WHERE kind = 'accepted'").Scan(&accepted)
	after := e.fake.Snapshot()[0]
	if accepted != 1 || after.Title == is.Title {
		t.Errorf("accepted %d (quer 1), título %q (deveria ter virado o achado)", accepted, after.Title)
	}
}
