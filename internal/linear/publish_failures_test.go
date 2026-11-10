package linear_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dev-danilocordeiro/sentinela/internal/linear"
)

// failingTracker falha o CreateIssue das issues cujo título contém um dos textos de fail, com a
// mensagem dada.
type failingTracker struct {
	linear.Tracker
	fail map[string]string
}

func (f *failingTracker) CreateIssue(ctx context.Context, in linear.IssueInput) (linear.Issue, error) {
	for title, msg := range f.fail {
		if strings.Contains(in.Title, title) {
			return linear.Issue{}, errors.New(msg)
		}
	}
	return f.Tracker.CreateIssue(ctx, in)
}

// Uma issue que falha não interrompe as outras: vira aviso, conta em Failed, o erro diz quantas
// ficaram de fora e a próxima rodada a publica.
func TestPublishFailureOfOneIssueDoesNotStopTheOthers(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	for i := range 3 {
		e.addFinding(t, fmt.Sprintf("fp%010d", i), fmt.Sprintf("libs/p%d/a.go", i), "high")
	}
	ft := &failingTracker{Tracker: e.cl, fail: map[string]string{"fp0000000001": "chamada ao MCP falhou: servidor ocupado"}}
	e.pub.Linear = ft
	res, err := e.pub.Publish(ctx, "acme/mono", 0, e.res)
	if !errors.Is(err, linear.ErrPublishFailed) {
		t.Fatalf("err = %v, quer ErrPublishFailed", err)
	}
	if len(res.Issues) != 2 || res.Failed != 1 || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "fica pendente") {
		t.Fatalf("issues = %d, falhas = %d, avisos = %v", len(res.Issues), res.Failed, res.Warnings)
	}
	ft.fail = nil
	res, err = e.pub.Publish(ctx, "acme/mono", 0, e.res)
	if err != nil || len(res.Issues) != 1 || !strings.Contains(res.Issues[0].Title, "fp0000000001") {
		t.Fatalf("a próxima rodada publica a que falhou: %+v, %v", res.Issues, err)
	}
}

// Limite de uso da assinatura: para na hora, guarda o horário do reset e deixa o resto pendente.
func TestPublishStopsAtTheUsageLimitAndKeepsTheResetTime(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	for i := range 3 {
		e.addFinding(t, fmt.Sprintf("fp%010d", i), fmt.Sprintf("libs/p%d/a.go", i), "high")
	}
	e.pub.Linear = &failingTracker{Tracker: e.cl, fail: map[string]string{
		"fp0000000001": "chamada ao MCP falhou (success): You've hit your session limit · resets 5:50pm (America/Sao_Paulo)",
	}}
	res, err := e.pub.Publish(ctx, "acme/mono", 0, e.res)
	if !errors.Is(err, linear.ErrUsagePaused) {
		t.Fatalf("err = %v, quer ErrUsagePaused", err)
	}
	if len(res.Issues) != 1 || res.PausedUntil == nil {
		t.Fatalf("issues = %d, pausa = %v", len(res.Issues), res.PausedUntil)
	}
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	if got := res.PausedUntil.In(loc); got.Hour() != 17 || got.Minute() != 50 {
		t.Errorf("reset = %v, quer 17:50 em São Paulo", got)
	}
	pending, err := e.st.Q.PendingFindings(ctx, "acme/mono")
	if err != nil || len(pending) != 2 {
		t.Errorf("pendentes = %d (%v), quer 2", len(pending), err)
	}
}
