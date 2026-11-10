package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dev-danilocordeiro/sentinela/internal/llm"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

func TestNanoUSDRoundTripsThroughNumeric(t *testing.T) {
	for _, v := range []int64{0, 1, 535_000, 5_000_000_000, 123_456_789_012} {
		got, err := NumericToNanoUSD(NanoUSDToNumeric(v))
		if err != nil || got != v {
			t.Errorf("%d -> %d (%v)", v, got, err)
		}
	}
	got, err := NumericToNanoUSD(pgtype.Numeric{Int: NanoUSDToNumeric(5).Int, Exp: -6, Valid: true})
	if err != nil || got != 5_000 {
		t.Errorf("escala -6: %d (%v)", got, err)
	}
}

// Teste de integração: precisa de um Postgres com pgvector descartável.
// Ex.: docker compose up -d postgres && SENTINELA_TEST_DATABASE_URL=$DATABASE_URL go test ./internal/store
func TestMigrationsUpDownAndLLMCallRecordingAgainstPostgres(t *testing.T) {
	url := os.Getenv("SENTINELA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SENTINELA_TEST_DATABASE_URL não definida")
	}
	ctx := context.Background()
	m, err := NewMigrator(url)
	if err != nil {
		t.Fatal(err)
	}
	// Cleanup (não defer): precisa rodar o DownTo antes de fechar a conexão.
	t.Cleanup(func() {
		if _, err := m.DownTo(ctx, 0); err != nil {
			t.Errorf("limpando: %v", err)
		}
		_ = m.Close()
	})
	if _, err := m.DownTo(ctx, 0); err != nil {
		t.Fatalf("limpando: %v", err)
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}

	s, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run, err := s.Q.CreateRun(ctx, db.CreateRunParams{Repo: "o/r", BudgetUsd: NanoUSDToNumeric(5_000_000_000), Mode: "full"})
	if err != nil {
		t.Fatal(err)
	}
	rec := NewLLMRecorder(s)
	for i := 0; i < 2; i++ {
		err := rec.RecordCall(ctx, llm.CallRecord{
			RunID: run.ID, Agent: "package_triager", Model: "claude-haiku-5-5", PromptVersion: "v1",
			Mode: "batch", BatchID: "msgbatch_x", CustomID: "pkg", Status: "succeeded",
			Usage: llm.Usage{InputTokens: 10, OutputTokens: 5}, Cost: 535_000, Latency: 1500 * time.Millisecond,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	calls, err := s.Q.ListLLMCallsByRun(ctx, pgtype.Int8{Int64: run.ID, Valid: true})
	if err != nil || len(calls) != 2 {
		t.Fatalf("llm_calls = %d (%v)", len(calls), err)
	}
	got, err := s.Q.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cost, _ := NumericToNanoUSD(got.CostUsd); cost != 1_070_000 {
		t.Errorf("custo da rodada = %d nano, quer 1070000", cost)
	}

	// Down de todas as migrations e up de novo: toda migration tem down funcional.
	if _, err := m.DownTo(ctx, 0); err != nil {
		t.Fatalf("down: %v", err)
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatalf("up de novo: %v", err)
	}
}
