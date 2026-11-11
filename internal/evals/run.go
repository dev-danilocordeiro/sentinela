package evals

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/trace/noop"

	"github.com/dev-danilocordeiro/sentinela/internal/agents"
	"github.com/dev-danilocordeiro/sentinela/internal/agents/judge"
	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/discovery"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
)

// Settings are the Judge settings the run mirrors from the Sentinela config.
type Settings struct {
	Model         string
	MinConfidence float64
	Votes         int     // discovery.judge_votes
	RevoteBand    float64 // discovery.revote_band
	Refusal       config.RefusalPolicy
}

// SettingsFrom reads the Judge settings from a loaded config.
func SettingsFrom(cfg *config.Config) Settings {
	return Settings{
		Model:         cfg.Models.Judge,
		MinConfidence: cfg.Discovery.MinConfidence,
		Votes:         cfg.Discovery.JudgeVotes,
		RevoteBand:    cfg.Discovery.RevoteBand,
		Refusal:       cfg.Models.Refusal,
	}
}

// Prediction values. A product question is published (accepted); merge cannot happen without
// similar findings, so any other outcome counts as rejected.
const (
	PredAccepted = Accepted
	PredRejected = Rejected
	PredError    = "error"
)

// Result is the Judge's answer for one case.
type Result struct {
	ID         string  `json:"id"`
	Category   string  `json:"category"`
	Label      string  `json:"label"`
	Prediction string  `json:"prediction"`
	Decision   string  `json:"decision,omitempty"`   // publish | discard | merge after discovery.Decide
	Question   bool    `json:"question,omitempty"`   // published as a product question
	Confidence float64 `json:"confidence,omitempty"` // of the verdict that counted
	Votes      int     `json:"votes,omitempty"`
	Reason     string  `json:"reason,omitempty"`
	Error      string  `json:"error,omitempty"`
}

// Correct reports whether the prediction matches the label.
func (r Result) Correct() bool { return r.Prediction == r.Label }

// Run asks the Judge about every case through caller and applies the pipeline's decision rule.
// An item the model skips (refusal, invalid output) becomes PredError; only run-level failures
// (budget, API down) return an error.
func Run(ctx context.Context, caller llm.Caller, ds Dataset, s Settings) ([]Result, error) {
	tracer := noop.NewTracerProvider().Tracer("evals")
	items := make([]agents.Item, 0, len(ds.Cases))
	for _, c := range ds.Cases {
		req, err := judge.Request(judge.Input{Finding: c.Finding, MinConfidence: s.MinConfidence}, judge.Options{Model: s.Model})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.ID, err)
		}
		items = append(items, agents.Item{Key: c.ID, Request: req, Fallback: s.Refusal.FallbackModel})
	}
	outcomes, err := agents.RunAll(ctx, caller, tracer, judge.Name, items, s.Refusal)
	if err != nil {
		return nil, err
	}

	first := map[string]judge.Verdict{}
	failed := map[string]string{}
	for _, it := range items {
		o := outcomes[it.Key]
		if o.Skipped {
			failed[it.Key] = o.Reason
			continue
		}
		v, perr := judge.Parse(o.Response)
		if perr != nil {
			failed[it.Key] = perr.Error()
			continue
		}
		first[it.Key] = v
	}

	// Extra votes for verdicts near the threshold, as discovery does (median of all votes).
	byKey := map[string]agents.Item{}
	for _, it := range items {
		byKey[it.Key] = it
	}
	var extra []agents.Item
	for _, it := range items {
		v, ok := first[it.Key]
		if !ok || s.Votes < 2 || !discovery.Borderline(v, s.MinConfidence, s.RevoteBand) {
			continue
		}
		for n := 2; n <= s.Votes; n++ {
			extra = append(extra, agents.Item{Key: voteKey(it.Key, n), Request: it.Request, Fallback: it.Fallback})
		}
	}
	votes := map[string][]judge.Verdict{}
	for k, v := range first {
		votes[k] = []judge.Verdict{v}
	}
	if len(extra) > 0 {
		more, err := agents.RunAll(ctx, caller, tracer, judge.Name, extra, s.Refusal)
		if err != nil {
			return nil, err
		}
		for _, it := range extra {
			o := more[it.Key]
			if o.Skipped {
				continue
			}
			if v, perr := judge.Parse(o.Response); perr == nil {
				key := it.Key[:strings.LastIndex(it.Key, "#vote")]
				votes[key] = append(votes[key], v)
			}
		}
	}

	results := make([]Result, 0, len(ds.Cases))
	for _, c := range ds.Cases {
		r := Result{ID: c.ID, Category: c.Category, Label: c.Label}
		vs, ok := votes[c.ID]
		if !ok {
			r.Prediction, r.Error = PredError, failed[c.ID]
			results = append(results, r)
			continue
		}
		v := vs[0]
		if len(vs) > 1 {
			v = discovery.MedianVote(vs)
		}
		decision, _ := discovery.Decide(s.MinConfidence, v)
		r.Decision, r.Question, r.Confidence, r.Votes, r.Reason = decision, v.Premise != "" && decision == judge.Publish, v.Confidence, len(vs), v.Reason
		r.Prediction = PredRejected
		if decision == judge.Publish {
			r.Prediction = PredAccepted
		}
		results = append(results, r)
	}
	return results, nil
}

func voteKey(id string, n int) string { return id + "#vote" + strconv.Itoa(n) }

// MemoryRecorder keeps llm_calls in memory: the eval needs no database.
type MemoryRecorder struct {
	mu    sync.Mutex
	Calls []llm.CallRecord
}

func (m *MemoryRecorder) RecordCall(_ context.Context, rec llm.CallRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, rec)
	return nil
}

// Totals sums tokens and cost of the successful calls.
func (m *MemoryRecorder) Totals() (calls int, usage llm.Usage, cost llm.NanoUSD) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.Calls {
		calls++
		usage.InputTokens += c.Usage.InputTokens
		usage.OutputTokens += c.Usage.OutputTokens
		usage.CacheReadTokens += c.Usage.CacheReadTokens
		usage.CacheWrite5mTokens += c.Usage.CacheWrite5mTokens
		usage.CacheWrite1hTokens += c.Usage.CacheWrite1hTokens
		cost += c.Cost
	}
	return calls, usage, cost
}

// ErrStaleRecording means the request changed (prompt, schema or dataset) since it was recorded.
var ErrStaleRecording = errors.New("recording is stale: request changed since the live run")
