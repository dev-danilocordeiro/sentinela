package local

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
)

// M9 (revisão 2026-10-11): dois `finding accept` ao mesmo tempo gravam um accepted só. Aqui o
// "outro processo" é uma transação do teste que trava o achado e grava o accepted; o Accept tem
// de esperar por ela e ver o accepted, em vez de conferir antes e gravar um segundo.
func TestConcurrentAcceptsRecordASingleAccepted(t *testing.T) {
	tr, st := setup(t, config.TrackerLocal)
	ctx := context.Background()
	id := addFinding(t, st, 0, "dddddddddddd", "Valida o tamanho do corpo")
	tx, err := st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT 1 FROM findings WHERE id = $1 FOR UPDATE", id); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO outcomes (finding_id, kind, actor) VALUES ($1, 'accepted', 'outro')", id); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- tr.Accept(ctx, id, Decision{Actor: "dono"}) }()
	select {
	case err := <-done:
		t.Fatalf("o Accept não esperou a outra transação: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil || !strings.Contains(err.Error(), "já foi aceito") {
		t.Errorf("Accept depois da outra transação: %v (quer \"já foi aceito\")", err)
	}
	var n int
	if err := st.Pool.QueryRow(ctx, "SELECT count(*) FROM outcomes WHERE finding_id = $1 AND kind = 'accepted'", id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("accepted = %d, quer 1", n)
	}
}
