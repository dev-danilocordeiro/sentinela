package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dev-danilocordeiro/sentinela/internal/llm"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// LLMRecorder grava cada chamada em llm_calls e soma o custo na rodada, na mesma transação.
type LLMRecorder struct {
	s *Store
}

func NewLLMRecorder(s *Store) *LLMRecorder { return &LLMRecorder{s: s} }

var _ llm.Recorder = (*LLMRecorder)(nil)

func (r *LLMRecorder) RecordCall(ctx context.Context, rec llm.CallRecord) error {
	return pgx.BeginFunc(ctx, r.s.Pool, func(tx pgx.Tx) error {
		q := r.s.Q.WithTx(tx)
		var runID pgtype.Int8
		if rec.RunID != 0 {
			runID = pgtype.Int8{Int64: rec.RunID, Valid: true}
		}
		_, err := q.InsertLLMCall(ctx, db.InsertLLMCallParams{
			RunID:              runID,
			Agent:              rec.Agent,
			Model:              rec.Model,
			PromptVersion:      rec.PromptVersion,
			Mode:               rec.Mode,
			BatchID:            text(rec.BatchID),
			CustomID:           text(rec.CustomID),
			MessageID:          text(rec.MessageID),
			Status:             rec.Status,
			StopReason:         text(rec.StopReason),
			InputTokens:        rec.Usage.InputTokens,
			OutputTokens:       rec.Usage.OutputTokens,
			CacheWrite5mTokens: rec.Usage.CacheWrite5mTokens,
			CacheWrite1hTokens: rec.Usage.CacheWrite1hTokens,
			CacheReadTokens:    rec.Usage.CacheReadTokens,
			CostUsd:            NanoUSDToNumeric(int64(rec.Cost)),
			LatencyMs:          rec.Latency.Milliseconds(),
			Error:              text(rec.Error),
			Provider:           provider(rec.Provider),
		})
		if err != nil {
			return fmt.Errorf("inserindo llm_call: %w", err)
		}
		if rec.RunID != 0 && rec.Cost != 0 {
			if _, err := q.AddRunCost(ctx, db.AddRunCostParams{ID: rec.RunID, Delta: NanoUSDToNumeric(int64(rec.Cost))}); err != nil {
				return fmt.Errorf("somando custo na rodada: %w", err)
			}
		}
		return nil
	})
}

func text(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}

func provider(p string) string {
	if p == "" {
		return "anthropic_api"
	}
	return p
}
