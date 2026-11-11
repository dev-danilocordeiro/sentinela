package evals

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Metrics is the agreement for a group of cases. Errors count as wrong in Accuracy.
type Metrics struct {
	Cases    int     `json:"cases"`
	Correct  int     `json:"correct"`
	Errors   int     `json:"errors"`
	TP       int     `json:"tp"` // labeled accepted, predicted accepted
	FP       int     `json:"fp"` // labeled rejected, predicted accepted
	TN       int     `json:"tn"` // labeled rejected, predicted rejected
	FN       int     `json:"fn"` // labeled accepted, predicted rejected
	Accuracy float64 `json:"accuracy"`
	// Precision and Recall of "accepted": of what the Judge would publish, how much reviewers keep;
	// of what reviewers keep, how much the Judge publishes. -1 when undefined (no denominator).
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
}

func score(rs []Result) Metrics {
	var m Metrics
	for _, r := range rs {
		m.Cases++
		switch {
		case r.Prediction == PredError:
			m.Errors++
		case r.Label == Accepted && r.Prediction == PredAccepted:
			m.TP++
		case r.Label == Rejected && r.Prediction == PredAccepted:
			m.FP++
		case r.Label == Rejected && r.Prediction == PredRejected:
			m.TN++
		case r.Label == Accepted && r.Prediction == PredRejected:
			m.FN++
		}
	}
	m.Correct = m.TP + m.TN
	m.Accuracy, m.Precision, m.Recall = ratio(m.Correct, m.Cases), ratio(m.TP, m.TP+m.FP), ratio(m.TP, m.TP+m.FN)
	return m
}

func ratio(a, b int) float64 {
	if b == 0 {
		return -1
	}
	return math.Round(float64(a)/float64(b)*1000) / 1000
}

// Cost is what the live run spent. With the claude_code provider it is the list-price equivalent
// that Claude Code reports, not money charged.
type Cost struct {
	Calls        int     `json:"calls"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	USD          float64 `json:"usd"`
}

// Report is the outcome of one run, written as JSON (machine-checked in CI) and Markdown.
type Report struct {
	Date          string             `json:"date"`
	Provider      string             `json:"provider"`
	Model         string             `json:"model"`
	PromptVersion string             `json:"prompt_version"`
	DatasetSHA256 string             `json:"dataset_sha256"`
	MinConfidence float64            `json:"min_confidence"`
	Votes         int                `json:"judge_votes"`
	Overall       Metrics            `json:"overall"`
	ByCategory    map[string]Metrics `json:"by_category"`
	Cost          *Cost              `json:"cost,omitempty"`
	Results       []Result           `json:"results"`
}

// NewReport scores results.
func NewReport(now time.Time, provider, promptVersion string, ds Dataset, s Settings, rs []Result) Report {
	rep := Report{
		Date: now.UTC().Format("2006-01-02"), Provider: provider, Model: s.Model, PromptVersion: promptVersion,
		DatasetSHA256: ds.SHA256, MinConfidence: s.MinConfidence, Votes: s.Votes,
		Overall: score(rs), ByCategory: map[string]Metrics{}, Results: rs,
	}
	for _, c := range Categories {
		var sub []Result
		for _, r := range rs {
			if r.Category == c {
				sub = append(sub, r)
			}
		}
		if len(sub) > 0 {
			rep.ByCategory[c] = score(sub)
		}
	}
	return rep
}

// Compare checks that a replayed report reproduces a recorded one: same dataset, prompt, model and
// the same prediction for every case. Cost and date are not compared.
func Compare(want, got Report) []string {
	var diffs []string
	if want.DatasetSHA256 != got.DatasetSHA256 {
		diffs = append(diffs, "dataset changed since the recorded run (sha256 differs)")
	}
	if want.PromptVersion != got.PromptVersion {
		diffs = append(diffs, fmt.Sprintf("prompt version %s, recorded run used %s", got.PromptVersion, want.PromptVersion))
	}
	if want.Model != got.Model {
		diffs = append(diffs, fmt.Sprintf("model %s, recorded run used %s", got.Model, want.Model))
	}
	pred := map[string]Result{}
	for _, r := range got.Results {
		pred[r.ID] = r
	}
	for _, w := range want.Results {
		g, ok := pred[w.ID]
		switch {
		case !ok:
			diffs = append(diffs, fmt.Sprintf("%s: missing from replay", w.ID))
		case g.Prediction != w.Prediction || g.Decision != w.Decision:
			diffs = append(diffs, fmt.Sprintf("%s: recorded %s/%s, replay %s/%s %s", w.ID, w.Prediction, w.Decision, g.Prediction, g.Decision, g.Error))
		}
	}
	if len(got.Results) != len(want.Results) {
		diffs = append(diffs, fmt.Sprintf("replay scored %d cases, recorded run %d", len(got.Results), len(want.Results)))
	}
	if want.Overall != got.Overall {
		diffs = append(diffs, fmt.Sprintf("overall metrics differ: recorded %+v, replay %+v", want.Overall, got.Overall))
	}
	return diffs
}

func pct(v float64) string {
	if v < 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", v*100)
}

// Markdown renders the report for humans.
func (r Report) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Judge eval: %s\n\n", r.Date)
	fmt.Fprintf(&b, "- Model: `%s` (prompt `%s`), provider `%s`\n", r.Model, r.PromptVersion, r.Provider)
	fmt.Fprintf(&b, "- Decision rule: publish at confidence >= %.2f, %d vote(s) near the threshold (median)\n", r.MinConfidence, r.Votes)
	fmt.Fprintf(&b, "- Dataset: %d cases, sha256 `%s`\n", r.Overall.Cases, short(r.DatasetSHA256))
	if r.Cost != nil {
		fmt.Fprintf(&b, "- Cost: %d calls, %d input / %d output tokens, $%.4f", r.Cost.Calls, r.Cost.InputTokens, r.Cost.OutputTokens, r.Cost.USD)
		if r.Provider == "claude_code" {
			b.WriteString(" (list-price equivalent reported by Claude Code; not charged on a subscription)")
		}
		b.WriteString("\n")
	}
	b.WriteString("\n| Category | Cases | Correct | Accuracy | Precision (accepted) | Recall (accepted) | Errors |\n|---|---|---|---|---|---|---|\n")
	row := func(name string, m Metrics) {
		fmt.Fprintf(&b, "| %s | %d | %d | %s | %s | %s | %d |\n", name, m.Cases, m.Correct, pct(m.Accuracy), pct(m.Precision), pct(m.Recall), m.Errors)
	}
	for _, c := range Categories {
		if m, ok := r.ByCategory[c]; ok {
			row(c, m)
		}
	}
	row("**all**", r.Overall)

	o := r.Overall
	b.WriteString("\nConfusion matrix (rows: label, columns: Judge):\n\n| | published | not published |\n|---|---|---|\n")
	fmt.Fprintf(&b, "| accepted | %d | %d |\n| rejected | %d | %d |\n", o.TP, o.FN, o.FP, o.TN)

	var misses []Result
	for _, res := range r.Results {
		if !res.Correct() {
			misses = append(misses, res)
		}
	}
	sort.SliceStable(misses, func(i, j int) bool { return misses[i].ID < misses[j].ID })
	if len(misses) > 0 {
		b.WriteString("\n## Disagreements\n\n| Case | Label | Judge | Confidence | Reason |\n|---|---|---|---|---|\n")
		for _, m := range misses {
			judge, reason := m.Prediction, m.Reason
			if m.Question {
				judge += " (product question)"
			}
			if m.Prediction == PredError {
				reason = m.Error
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %.2f | %s |\n", m.ID, m.Label, judge, m.Confidence, cell(reason))
		}
	}
	return b.String()
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func cell(s string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ").Replace(s)
}
