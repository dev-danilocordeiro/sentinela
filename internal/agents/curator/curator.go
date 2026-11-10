// Package curator é o Feedback Curator: Haiku, uma chamada por repo por semana, transforma as
// decisões humanas sobre as issues em exemplos few-shot para o Judge, supressões por padrão de
// caminho e limiares de confiança por módulo e categoria.
package curator

import (
	"fmt"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/dev-danilocordeiro/sentinela/internal/agents"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
)

const Name = "curator"

var (
	prompt    = agents.MustPrompt(Name)
	inSchema  = agents.MustSchema(Name + ".input")
	OutSchema = agents.MustSchema(Name + ".output")
)

func PromptVersion() agents.Prompt { return prompt }

type Outcome struct {
	FindingID         int64   `json:"finding_id"`
	Module            string  `json:"module"`
	Category          string  `json:"category"`
	Severity          string  `json:"severity"`
	Title             string  `json:"title"`
	File              string  `json:"file"`
	Outcome           string  `json:"outcome"` // accepted | rejected
	Reason            string  `json:"reason"`
	AnalystConfidence float64 `json:"analyst_confidence"`
	JudgeConfidence   float64 `json:"judge_confidence"`
}

type Example struct {
	FindingID int64  `json:"finding_id"`
	Outcome   string `json:"outcome"`
	Title     string `json:"title"`
	Lesson    string `json:"lesson"`
}

type Threshold struct {
	Module        string  `json:"module"`
	Category      string  `json:"category"`
	MinConfidence float64 `json:"min_confidence"`
	Reason        string  `json:"reason"`
}

type Input struct {
	Repo                 string      `json:"repo"`
	DefaultMinConfidence float64     `json:"default_min_confidence"`
	Outcomes             []Outcome   `json:"outcomes"`
	CurrentExamples      []Example   `json:"current_examples"`
	CurrentThresholds    []Threshold `json:"current_thresholds"`
	MaxExamples          int         `json:"max_examples"`
}

type ExampleOut struct {
	FindingID int64  `json:"finding_id"`
	Lesson    string `json:"lesson"`
}

type Suppression struct {
	Module   string `json:"module"`
	Category string `json:"category"`
	Pattern  string `json:"pattern"`
	Reason   string `json:"reason"`
}

type Output struct {
	Examples     []ExampleOut  `json:"examples"`
	Suppressions []Suppression `json:"suppressions"`
	Thresholds   []Threshold   `json:"thresholds"`
}

// Limites que o código impõe ao que o modelo propõe.
const (
	MinThreshold = 0.5
	MaxThreshold = 0.95
)

type Options struct {
	Model string
}

const MaxTokens = 4000

func Request(in Input, o Options) (llm.Request, error) {
	in.Outcomes, in.CurrentExamples, in.CurrentThresholds = agents.Slice(in.Outcomes), agents.Slice(in.CurrentExamples), agents.Slice(in.CurrentThresholds)
	user, err := agents.EncodeInput(inSchema, in)
	if err != nil {
		return llm.Request{}, err
	}
	return llm.Request{
		Agent: Name, PromptVersion: prompt.Version, Model: o.Model,
		System: prompt.Body, User: user, MaxTokens: MaxTokens,
		Schema: OutSchema.Raw, Effort: "low",
	}, nil
}

// Parse valida a saída e descarta o que fere as regras da casa, explicando cada descarte:
// exemplo de achado que não estava na entrada, glob inválido ou que cobre o repo inteiro,
// limiar fora de [0,5; 0,95] (é ajustado ao limite).
func Parse(resp llm.Response, in Input) (Output, []string, error) {
	var out Output
	if err := agents.Decode(OutSchema, resp, &out); err != nil {
		return Output{}, nil, err
	}
	var notes []string
	known := map[int64]bool{}
	for _, o := range in.Outcomes {
		known[o.FindingID] = true
	}
	for _, e := range in.CurrentExamples {
		known[e.FindingID] = true
	}
	var examples []ExampleOut
	for _, e := range out.Examples {
		switch {
		case !known[e.FindingID]:
			notes = append(notes, fmt.Sprintf("exemplo do achado %d ignorado: não estava na entrada", e.FindingID))
		case len(examples) >= in.MaxExamples:
			notes = append(notes, fmt.Sprintf("exemplo do achado %d ignorado: passou de max_examples", e.FindingID))
		default:
			examples = append(examples, e)
		}
	}
	var sups []Suppression
	for _, s := range out.Suppressions {
		if why := badPattern(s.Pattern); why != "" {
			notes = append(notes, fmt.Sprintf("supressão %q ignorada: %s", s.Pattern, why))
			continue
		}
		sups = append(sups, s)
	}
	ths := make([]Threshold, 0, len(out.Thresholds))
	for _, t := range out.Thresholds {
		clamped := min(max(t.MinConfidence, MinThreshold), MaxThreshold)
		if clamped != t.MinConfidence {
			notes = append(notes, fmt.Sprintf("limiar %s/%s ajustado de %.2f para %.2f", t.Module, t.Category, t.MinConfidence, clamped))
			t.MinConfidence = clamped
		}
		ths = append(ths, t)
	}
	return Output{Examples: agents.Slice(examples), Suppressions: agents.Slice(sups), Thresholds: ths}, notes, nil
}

// badPattern recusa globs inválidos e os que casam com qualquer arquivo do repo.
func badPattern(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || !doublestar.ValidatePattern(p) {
		return "glob inválido"
	}
	for _, probe := range []string{"main.go", "src/a/b/C.java", "x/y.py"} {
		ok, _ := doublestar.Match(p, probe)
		if !ok {
			return ""
		}
	}
	return "cobre o repo inteiro"
}
