// Package architect é o System Architect: Sonnet, uma chamada por rodada, procura
// problemas entre módulos a partir do grafo de dependências e dos resumos dos pacotes.
package architect

import (
	"context"
	"fmt"

	"github.com/dev-danilocordeiro/sentinela/internal/agents"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
)

const Name = "architect"

var (
	prompt    = agents.MustPrompt(Name)
	inSchema  = agents.MustSchema(Name + ".input")
	OutSchema = agents.MustSchema(Name + ".output")
)

func PromptVersion() agents.Prompt { return prompt }

type Package struct {
	Path      string     `json:"path"`
	Language  string     `json:"language"`
	OwnerTeam string     `json:"owner_team"`
	Summary   string     `json:"summary"`
	Landmarks []Landmark `json:"landmarks"`
}

// Landmark é uma linha real do pacote (import ou declaração), para o Architect citar como
// evidência: o código descarta evidência cujo snippet não aparece no arquivo.
type Landmark struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Code string `json:"code"`
}

type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type KnownFinding struct {
	Title    string `json:"title"`
	Category string `json:"category"`
	File     string `json:"file"`
}

type Input struct {
	Repo        string         `json:"repo"`
	Packages    []Package      `json:"packages"`
	Edges       []Edge         `json:"edges"`
	Findings    []KnownFinding `json:"findings"`
	MaxFindings int            `json:"max_findings"`
}

type Output struct {
	Findings []agents.FindingOut `json:"findings"`
}

type Options struct {
	Model string
}

const MaxTokens = 16000

func Request(in Input, o Options) (llm.Request, error) {
	in.Packages, in.Edges, in.Findings = agents.Slice(in.Packages), agents.Slice(in.Edges), agents.Slice(in.Findings)
	for i := range in.Packages {
		in.Packages[i].Landmarks = agents.Slice(in.Packages[i].Landmarks)
	}
	user, err := agents.EncodeInput(inSchema, in)
	if err != nil {
		return llm.Request{}, err
	}
	return llm.Request{
		Agent: Name, PromptVersion: prompt.Version, Model: o.Model,
		System: prompt.Body, User: user, MaxTokens: MaxTokens,
		Schema: OutSchema.Raw, Effort: "medium",
	}, nil
}

func Parse(resp llm.Response, maxFindings int) (Output, error) {
	var out Output
	if err := agents.Decode(OutSchema, resp, &out); err != nil {
		return Output{}, err
	}
	for i, f := range out.Findings {
		if f.Confidence < 0 || f.Confidence > 1 {
			return Output{}, fmt.Errorf("architect: achado %d com confidence %v fora de 0..1", i, f.Confidence)
		}
	}
	if maxFindings > 0 && len(out.Findings) > maxFindings {
		out.Findings = out.Findings[:maxFindings]
	}
	return out, nil
}

func Run(ctx context.Context, client *llm.Client, in Input, o Options) (Output, error) {
	req, err := Request(in, o)
	if err != nil {
		return Output{}, err
	}
	resp, err := client.Call(ctx, req)
	if err != nil {
		return Output{}, err
	}
	return Parse(resp, in.MaxFindings)
}
