package discovery

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/linear/lineartest"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// limitedTracker falha o CreateIssue com a mensagem de limite de uso enquanto limited.
type limitedTracker struct {
	linear.Tracker
	limited bool
}

func (l *limitedTracker) CreateIssue(ctx context.Context, in linear.IssueInput) (linear.Issue, error) {
	if l.limited {
		return linear.Issue{}, errors.New("chamada ao MCP falhou (success): You've hit your session limit · resets 5:50pm (America/Sao_Paulo)")
	}
	return l.Tracker.CreateIssue(ctx, in)
}

// Limite de uso na publicação (ADR 0055): a rodada guarda o horário do reset; depois dele,
// ResumePublish publica o pendente sem analisar o repo, e o repo sai da lista de pausados.
func TestPublicationPausedByTheUsageLimitResumesAfterTheReset(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	fl := lineartest.New()
	srv := httptest.NewServer(fl)
	t.Cleanup(srv.Close)
	lt := &limitedTracker{Tracker: linear.NewClient(srv.URL, "k"), limited: true}
	e.runner.Cfg.Publish.Enabled = true
	e.runner.Publisher = &linear.Publisher{Cfg: e.runner.Cfg, Store: e.st, Linear: lt}

	res, err := e.runner.Run(ctx, Options{Repo: "acme/svc"})
	if err != nil || !errors.Is(res.PublishErr, linear.ErrUsagePaused) {
		t.Fatalf("rodada: %v / %v", err, res.PublishErr)
	}
	paused, err := e.runner.PausedPublications(ctx)
	if err != nil || len(paused) != 1 || paused[0].Repo != "acme/svc" || paused[0].Until.IsZero() {
		t.Fatalf("pausados = %+v (%v)", paused, err)
	}
	calls := e.fake.calls.Load()

	lt.limited = false
	res, err = e.runner.ResumePublish(ctx, "acme/svc")
	if err != nil || res.PublishErr != nil || res.Stats.Issues == nil || len(res.Stats.Issues.Issues) != 1 || len(fl.Snapshot()) != 1 {
		t.Fatalf("retomada: %v / %v / %+v", err, res.PublishErr, res.Stats.Issues)
	}
	if e.fake.calls.Load() != calls {
		t.Error("a retomada não chama modelos")
	}
	if paused, _ := e.runner.PausedPublications(ctx); len(paused) != 0 {
		t.Errorf("depois de publicar, nada pausado: %+v", paused)
	}
}

// scan --all publica depois de fechar as rodadas: a pausa é gravada nelas depois.
func TestSetRunPublishPauseMarksAFinishedRun(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	res, err := e.runner.Run(ctx, Options{Repo: "acme/svc"})
	if err != nil {
		t.Fatal(err)
	}
	if paused, _ := e.runner.PausedPublications(ctx); len(paused) != 0 {
		t.Fatalf("nada pausado ainda: %+v", paused)
	}
	until := time.Date(2026, 10, 17, 20, 50, 0, 0, time.UTC)
	if err := e.st.Q.SetRunPublishPause(ctx, db.SetRunPublishPauseParams{ID: res.RunID, Until: pgtype.Timestamptz{Time: until, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	paused, err := e.runner.PausedPublications(ctx)
	if err != nil || len(paused) != 1 || !paused[0].Until.Equal(until) || paused[0].RunID != res.RunID {
		t.Fatalf("pausados = %+v (%v)", paused, err)
	}
}
