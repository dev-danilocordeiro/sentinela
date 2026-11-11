package evals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dev-danilocordeiro/sentinela/internal/llm"
)

// Recording is one model response saved by a live run, keyed by the batch custom id (case id,
// plus the vote number for extra votes). ReplayKey hashes the prompt version, system prompt and
// input, so a replay notices when any of them changed.
type Recording struct {
	ReplayKey  string    `json:"replay_key"`
	Model      string    `json:"model"`
	StopReason string    `json:"stop_reason"`
	Text       string    `json:"text"`
	Usage      llm.Usage `json:"usage"`
}

// Recorder wraps a live caller and saves every successful batch response under Dir.
type Recorder struct {
	llm.Caller
	Dir string
}

func (r *Recorder) Batch(ctx context.Context, items []llm.BatchItem) (map[string]llm.BatchResult, error) {
	out, err := r.Caller.Batch(ctx, items)
	if mkErr := os.MkdirAll(r.Dir, 0o750); mkErr != nil {
		return out, mkErr
	}
	for _, it := range items {
		res, ok := out[it.CustomID]
		if !ok || res.Err != nil {
			continue
		}
		rec := Recording{ReplayKey: llm.ReplayKey(it.Request), Model: res.Model, StopReason: res.StopReason, Text: res.Text, Usage: res.Usage}
		data, mErr := json.MarshalIndent(rec, "", "  ")
		if mErr != nil {
			return out, mErr
		}
		if wErr := os.WriteFile(filepath.Join(r.Dir, it.CustomID+".json"), append(data, '\n'), 0o600); wErr != nil {
			return out, wErr
		}
	}
	return out, err
}

// Replayer answers from recordings and never calls a model. A missing or stale recording fails
// that item, which the report counts as an error.
type Replayer struct {
	Dir string
	B   *llm.Budget
}

var _ llm.Caller = (*Replayer)(nil)

func (r *Replayer) Provider() string    { return "replay" }
func (r *Replayer) Budget() *llm.Budget { return r.B }

func (r *Replayer) Call(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("evals replay answers batches only")
}

func (r *Replayer) Batch(_ context.Context, items []llm.BatchItem) (map[string]llm.BatchResult, error) {
	out := make(map[string]llm.BatchResult, len(items))
	for _, it := range items {
		out[it.CustomID] = r.one(it)
	}
	return out, nil
}

func (r *Replayer) one(it llm.BatchItem) llm.BatchResult {
	data, err := os.ReadFile(filepath.Join(r.Dir, it.CustomID+".json")) //nolint:gosec // recordings dir comes from the operator
	if err != nil {
		return llm.BatchResult{Err: fmt.Errorf("%w: %s", llm.ErrNoRecording, it.CustomID)}
	}
	var rec Recording
	if err := json.Unmarshal(data, &rec); err != nil {
		return llm.BatchResult{Err: fmt.Errorf("%s: %w", it.CustomID, err)}
	}
	if rec.ReplayKey != llm.ReplayKey(it.Request) {
		return llm.BatchResult{Err: fmt.Errorf("%w: %s", ErrStaleRecording, it.CustomID)}
	}
	return llm.BatchResult{Response: llm.Response{Model: rec.Model, Text: rec.Text, StopReason: rec.StopReason, Usage: rec.Usage}}
}
