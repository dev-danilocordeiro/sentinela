package evals

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dev-danilocordeiro/sentinela/internal/agents/judge"
	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
)

// The shipped dataset loads, is balanced per category and label, and stays within 30 to 50 cases.
func TestShippedDatasetIsValidAndBalanced(t *testing.T) {
	ds, err := Load(filepath.Join("..", "..", "evals", "dataset", "findings.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(ds.Cases); n < 30 || n > 50 {
		t.Fatalf("dataset has %d cases, want 30..50", n)
	}
	count := map[string]int{}
	for _, c := range ds.Cases {
		count[c.Category+"/"+c.Label]++
	}
	for _, cat := range Categories {
		if count[cat+"/"+Accepted] == 0 || count[cat+"/"+Accepted] != count[cat+"/"+Rejected] {
			t.Errorf("%s: %d accepted, %d rejected; want a balanced, non-empty split", cat, count[cat+"/"+Accepted], count[cat+"/"+Rejected])
		}
	}
	for _, c := range ds.Cases {
		if _, err := judge.Request(judge.Input{Finding: c.Finding, MinConfidence: 0.6}, judge.Options{Model: "m"}); err != nil {
			t.Errorf("%s does not fit the judge input schema: %v", c.ID, err)
		}
	}
}

func TestLoadRejectsBadCases(t *testing.T) {
	good := `{"id":"a-1","label":"accepted","category":"bug","rationale":"r","finding":{"category":"bug","title":"t","evidence":[{"file":"f","line":1,"snippet":"s"}],"fingerprint":"abc"}}`
	for name, body := range map[string]string{
		"duplicate id":      good + "\n" + good,
		"bad label":         strings.Replace(good, `"accepted"`, `"maybe"`, 1),
		"category mismatch": strings.Replace(good, `"category":"bug","title"`, `"category":"security","title"`, 1),
		"unknown field":     strings.Replace(good, `"rationale"`, `"note":"x","rationale"`, 1),
		"no evidence":       strings.Replace(good, `[{"file":"f","line":1,"snippet":"s"}]`, `[]`, 1),
		"empty":             "\n",
	} {
		p := filepath.Join(t.TempDir(), "d.jsonl")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// fakeJudge answers by fingerprint; a list of answers is consumed one per call (for votes).
type fakeJudge struct {
	mu      sync.Mutex
	answers map[string][]string
	calls   int
}

func verdict(decision string, conf float64, premise string) string {
	pd := map[string]any{"depends": premise != "", "decision": premise}
	b, _ := json.Marshal(map[string]any{
		"decision": decision, "confidence": conf, "reason": "because", "merge_with": "",
		"related_to": []string{}, "product_decision": pd,
		"narrowed": map[string]any{"title": "", "context": "", "proposed_fix": "", "acceptance_criteria": []string{}},
	})
	return string(b)
}

func (f *fakeJudge) Provider() string    { return "fake" }
func (f *fakeJudge) Budget() *llm.Budget { return llm.NewCallBudget(0) }
func (f *fakeJudge) Call(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("batch only")
}

func (f *fakeJudge) Batch(_ context.Context, items []llm.BatchItem) (map[string]llm.BatchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]llm.BatchResult{}
	for _, it := range items {
		f.calls++
		var in struct {
			Finding struct {
				Fingerprint string `json:"fingerprint"`
			} `json:"finding"`
		}
		if err := json.Unmarshal([]byte(it.Request.User), &in); err != nil {
			out[it.CustomID] = llm.BatchResult{Err: err}
			continue
		}
		q := f.answers[in.Finding.Fingerprint]
		text := q[0]
		if len(q) > 1 {
			f.answers[in.Finding.Fingerprint] = q[1:]
		}
		out[it.CustomID] = llm.BatchResult{Response: llm.Response{Model: it.Request.Model, Text: text, StopReason: "end_turn", Usage: llm.Usage{InputTokens: 10, OutputTokens: 5}}}
	}
	return out, nil
}

func testCase(id, label, cat string) Case {
	return Case{ID: id, Label: label, Category: cat, Rationale: "r", Finding: judge.Finding{
		Source: "code_analysis", Repo: "example/x", Module: "m", Category: cat, Severity: "medium", Confidence: 0.8,
		Title: "t " + id, Context: "c", Evidence: []judge.Evidence{{File: "f.go", Line: 1, Snippet: "s"}},
		ProposedFix: "p", AcceptanceCriteria: []string{"a"}, Fingerprint: "fp-" + id,
	}}
}

func testRun() (Dataset, *fakeJudge, Settings) {
	ds := Dataset{SHA256: "sha", Cases: []Case{
		testCase("pub", Accepted, "bug"),          // publish 0.9: accepted, correct
		testCase("low", Accepted, "bug"),          // publish 0.4: below threshold, rejected, wrong
		testCase("disc", Rejected, "security"),    // discard: correct
		testCase("q", Rejected, "security"),       // discard with a product premise: published as question
		testCase("vote", Rejected, "performance"), // borderline publish, median of 3 votes is below the threshold
		testCase("bad", Accepted, "performance"),  // invalid output: error
	}}
	f := &fakeJudge{answers: map[string][]string{
		"fp-pub":  {verdict("publish", 0.9, "")},
		"fp-low":  {verdict("publish", 0.4, "")},
		"fp-disc": {verdict("discard", 0.8, "")},
		"fp-q":    {verdict("discard", 0.7, "Should anonymous users delete links?")},
		"fp-vote": {verdict("publish", 0.65, ""), verdict("publish", 0.5, ""), verdict("discard", 0.7, "")},
		"fp-bad":  {`{"decision":"publish"}`},
	}}
	return ds, f, Settings{Model: "m", MinConfidence: 0.6, Votes: 3, RevoteBand: 0.1, Refusal: config.RefusalPolicy{Action: config.RefusalSkip}}
}

func TestRunAppliesThePipelineDecisionRule(t *testing.T) {
	ds, f, s := testRun()
	rs, err := Run(context.Background(), f, ds, s)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Result{}
	for _, r := range rs {
		got[r.ID] = r
	}
	want := map[string]string{"pub": PredAccepted, "low": PredRejected, "disc": PredRejected, "q": PredAccepted, "vote": PredRejected, "bad": PredError}
	for id, p := range want {
		if got[id].Prediction != p {
			t.Errorf("%s: prediction %q, want %q (%+v)", id, got[id].Prediction, p, got[id])
		}
	}
	if !got["q"].Question {
		t.Error("q should be published as a product question")
	}
	if got["vote"].Votes != 3 || got["pub"].Votes != 1 {
		t.Errorf("votes: vote=%d pub=%d", got["vote"].Votes, got["pub"].Votes)
	}
	if f.calls != 8 {
		t.Errorf("model calls = %d, want 6 + 2 extra votes", f.calls)
	}

	rep := NewReport(time.Unix(0, 0), "fake", "judge@8", ds, s, rs)
	o := rep.Overall
	if o.Cases != 6 || o.TP != 1 || o.FN != 1 || o.TN != 2 || o.FP != 1 || o.Errors != 1 || o.Accuracy != 0.5 {
		t.Errorf("overall = %+v", o)
	}
	if o.Precision != 0.5 || o.Recall != 0.5 {
		t.Errorf("precision %v recall %v", o.Precision, o.Recall)
	}
	if b := rep.ByCategory["bug"]; b.Cases != 2 || b.Correct != 1 {
		t.Errorf("bug = %+v", b)
	}
	if _, ok := rep.ByCategory["improvement"]; ok {
		t.Error("a category without cases should not appear")
	}
	md := rep.Markdown()
	for _, s := range []string{"| bug | 2 | 1 | 50%", "| **all** | 6 | 3 | 50%", "| q | rejected | accepted (product question)", "| bad | accepted | error"} {
		if !strings.Contains(md, s) {
			t.Errorf("markdown lacks %q:\n%s", s, md)
		}
	}
}

// A live run's recordings replay to the same report; a changed request is reported as stale.
func TestRecordAndReplayReproduceTheReport(t *testing.T) {
	ds, f, s := testRun()
	dir := t.TempDir()
	live, err := Run(context.Background(), &Recorder{Caller: f, Dir: dir}, ds, s)
	if err != nil {
		t.Fatal(err)
	}
	want := NewReport(time.Unix(0, 0), "fake", "judge@8", ds, s, live)

	replayed, err := Run(context.Background(), &Replayer{Dir: dir, B: llm.NewCallBudget(0)}, ds, s)
	if err != nil {
		t.Fatal(err)
	}
	got := NewReport(time.Unix(0, 0), "replay", "judge@8", ds, s, replayed)
	if diffs := Compare(want, got); len(diffs) > 0 {
		t.Fatalf("replay differs: %v", diffs)
	}

	ds.Cases[0].Finding.Title = "edited after the live run"
	ds.SHA256 = "other"
	stale, err := Run(context.Background(), &Replayer{Dir: dir, B: llm.NewCallBudget(0)}, ds, s)
	if err != nil {
		t.Fatal(err)
	}
	if stale[0].Prediction != PredError || !strings.Contains(stale[0].Error, "stale") {
		t.Errorf("edited case should fail as stale: %+v", stale[0])
	}
	diffs := Compare(want, NewReport(time.Unix(0, 0), "replay", "judge@8", ds, s, stale))
	if len(diffs) < 2 || !strings.Contains(strings.Join(diffs, "\n"), "dataset changed") {
		t.Errorf("Compare should report the dataset change and the case: %v", diffs)
	}
}
