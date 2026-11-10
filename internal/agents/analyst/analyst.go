// Package analyst é o Package Analyst: Sonnet, em batch, um pacote sinalizado pelo
// Triager por chamada. Devolve achados com evidência arquivo:linha.
package analyst

import (
	"context"
	"fmt"

	"github.com/dev-danilocordeiro/sentinela/internal/agents"
	"github.com/dev-danilocordeiro/sentinela/internal/agents/triager"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
)

const Name = "analyst"

var (
	prompt    = agents.MustPrompt(Name)
	inSchema  = agents.MustSchema(Name + ".input")
	OutSchema = agents.MustSchema(Name + ".output")
)

func PromptVersion() agents.Prompt { return prompt }

type Triage struct {
	Category string `json:"category"`
	Reason   string `json:"reason"`
}

type Input struct {
	Repo           string               `json:"repo"`
	Package        string               `json:"package"`
	Language       string               `json:"language"`
	OwnerTeam      string               `json:"owner_team"`
	Files          []agents.File        `json:"files"`
	Truncated      []string             `json:"truncated"`
	Triage         Triage               `json:"triage"`
	ScannerSignals []agents.Signal      `json:"scanner_signals"`
	Suppressions   []agents.Suppression `json:"suppressions"`
	MaxFindings    int                  `json:"max_findings"`
}

type Output struct {
	Findings []agents.FindingOut `json:"findings"`
}

type Options struct {
	Model         string
	ContextTokens int
}

// MaxTokens dá espaço para o raciocínio do Sonnet em effort medium e vários achados.
const MaxTokens = 16000

func Request(in Input, o Options) (llm.Request, Input, error) {
	in.Files, in.Truncated = agents.Slice(in.Files), agents.Slice(in.Truncated)
	in.ScannerSignals, in.Suppressions = agents.Slice(in.ScannerSignals), agents.Slice(in.Suppressions)
	// Reaproveita o corte do Triager: mesmos campos de arquivos e sinais.
	fit := triager.Fit(triager.Input{Files: in.Files, Truncated: in.Truncated, ScannerSignals: in.ScannerSignals},
		o.ContextTokens-agents.EstimateTokens(prompt.Body)-2000)
	in.Files, in.Truncated, in.ScannerSignals = fit.Files, agents.Slice(fit.Truncated), fit.ScannerSignals
	user, err := agents.EncodeInput(inSchema, in)
	if err != nil {
		return llm.Request{}, in, err
	}
	return llm.Request{
		Agent: Name, PromptVersion: prompt.Version, Model: o.Model,
		System: prompt.Body, User: user, MaxTokens: MaxTokens,
		Schema: OutSchema.Raw, Effort: "medium",
	}, in, nil
}

// Parse valida e decodifica; achados além de max_findings são descartados.
func Parse(resp llm.Response, maxFindings int) (Output, error) {
	var out Output
	if err := agents.Decode(OutSchema, resp, &out); err != nil {
		return Output{}, err
	}
	for i, f := range out.Findings {
		if f.Confidence < 0 || f.Confidence > 1 {
			return Output{}, fmt.Errorf("analyst: achado %d com confidence %v fora de 0..1", i, f.Confidence)
		}
	}
	if maxFindings > 0 && len(out.Findings) > maxFindings {
		out.Findings = out.Findings[:maxFindings]
	}
	return out, nil
}

func Run(ctx context.Context, client *llm.Client, in Input, o Options) (Output, error) {
	req, in, err := Request(in, o)
	if err != nil {
		return Output{}, err
	}
	resp, err := client.Call(ctx, req)
	if err != nil {
		return Output{}, err
	}
	return Parse(resp, in.MaxFindings)
}
