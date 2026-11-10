package linear_test

import (
	"context"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/linear"
)

// M1 (revisão 2026-10-11): a resposta "não é intencional" já grava accepted; concluir a issue
// depois (corrigido à mão) fecha o achado como fixed sem um segundo accepted.
func TestCompletingAfterNotIntentionalDoesNotRecordASecondAccepted(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.cfg.Labels.Question, e.cfg.Labels.Intentional, e.cfg.Labels.NotIntentional = "sentinela-pergunta", "sentinela-intencional", "sentinela-nao-intencional"
	id := e.addFinding(t, "m1premissa01", "libs/m/a.go", "medium")
	if _, err := e.st.Pool.Exec(ctx, "UPDATE findings SET premise = 'É de propósito?' WHERE id = $1", id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pub.Publish(ctx, "acme/mono", 0, e.res); err != nil {
		t.Fatal(err)
	}
	ident := e.fake.Snapshot()[0].Identifier
	e.fake.AddLabel(ident, "sentinela-nao-intencional")
	syncer := &linear.Syncer{Store: e.st, Linear: e.cl, Cfg: e.cfg}
	if _, err := syncer.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	e.fake.Move(ident, "Done")
	if _, err := syncer.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	var accepted int
	_ = e.st.Pool.QueryRow(ctx, "SELECT state FROM findings WHERE id = $1", id).Scan(&state)
	_ = e.st.Pool.QueryRow(ctx, "SELECT count(*) FROM outcomes WHERE finding_id = $1 AND kind = 'accepted'", id).Scan(&accepted)
	if state != "fixed" || accepted != 1 {
		t.Errorf("achado %s com %d accepted (quer fixed e 1)", state, accepted)
	}
}
