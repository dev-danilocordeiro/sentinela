// Package triager é o Package Triager: Haiku, em batch, um pacote por chamada.
// Decide se o pacote merece análise profunda e resume o que ele faz.
package triager

import (
	"context"
	"fmt"
	"sort"

	"github.com/dev-danilocordeiro/sentinela/internal/agents"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
)

const Name = "triager"

var (
	prompt   = agents.MustPrompt(Name)
	inSchema = agents.MustSchema(Name + ".input")
	// OutSchema vai em output_config.format.
	OutSchema = agents.MustSchema(Name + ".output")
)

// PromptVersion é gravada em llm_calls e prompt_versions.
func PromptVersion() agents.Prompt { return prompt }

// Tipos compartilhados com o Analyst.
type (
	File        = agents.File
	Signal      = agents.Signal
	Suppression = agents.Suppression
)

type Neighbor struct {
	Package    string `json:"package"`
	Relation   string `json:"relation"` // imports | imported_by
	Signatures string `json:"signatures"`
}

type Input struct {
	Repo           string        `json:"repo"`
	Package        string        `json:"package"`
	Language       string        `json:"language"`
	OwnerTeam      string        `json:"owner_team"`
	Files          []File        `json:"files"`
	Truncated      []string      `json:"truncated"`
	Neighbors      []Neighbor    `json:"neighbors"`
	ScannerSignals []Signal      `json:"scanner_signals"`
	Suppressions   []Suppression `json:"suppressions"`
}

type Output struct {
	Category string `json:"category"`
	Deepen   bool   `json:"deepen"`
	Reason   string `json:"reason"`
	Summary  string `json:"summary"`
}

// Options do agente na rodada.
type Options struct {
	Model         string
	ContextTokens int // teto estimado de tokens da chamada inteira (system + user)
}

// MaxTokens cobre o raciocínio adaptativo em effort low e a resposta curta.
const MaxTokens = 4000

// Request monta a chamada, cortando o contexto para caber em ContextTokens.
// Devolve também a entrada efetivamente enviada (com Truncated preenchido).
func Request(in Input, o Options) (llm.Request, Input, error) {
	in.Files, in.Truncated, in.Neighbors = agents.Slice(in.Files), agents.Slice(in.Truncated), agents.Slice(in.Neighbors)
	in.ScannerSignals, in.Suppressions = agents.Slice(in.ScannerSignals), agents.Slice(in.Suppressions)
	in = Fit(in, o.ContextTokens-agents.EstimateTokens(prompt.Body))
	user, err := agents.EncodeInput(inSchema, in)
	if err != nil {
		return llm.Request{}, in, err
	}
	req := llm.Request{
		Agent: Name, PromptVersion: prompt.Version, Model: o.Model,
		System: prompt.Body, User: user, MaxTokens: MaxTokens,
		Schema: OutSchema.Raw, Effort: "low",
	}
	if est := agents.EstimateTokens(req.System) + agents.EstimateTokens(req.User); est > o.ContextTokens {
		return llm.Request{}, in, fmt.Errorf("triager: %s ainda tem ~%d tokens após o corte (teto %d)", in.Package, est, o.ContextTokens)
	}
	return req, in, nil
}

// Fit corta a entrada até caber em budget tokens estimados. Ordem de corte, do menos
// para o mais importante: vizinhos, sinais de scanner além dos 50 primeiros e, por fim,
// o conteúdo dos maiores arquivos (mantendo o começo). Cada corte fica em Truncated.
func Fit(in Input, budget int) Input {
	size := func() int {
		data, _ := agents.EncodeInputUnchecked(in)
		return agents.EstimateTokens(data)
	}
	if size() <= budget {
		return in
	}
	for len(in.Neighbors) > 0 && size() > budget {
		in.Neighbors = in.Neighbors[:len(in.Neighbors)-1]
	}
	if len(in.ScannerSignals) > 50 && size() > budget {
		in.ScannerSignals = in.ScannerSignals[:50]
	}
	if size() <= budget {
		return in
	}
	// Corta primeiro os arquivos maiores, pela metade a cada passo; abaixo de 2 KB, omite.
	order := make([]int, len(in.Files))
	for i := range order {
		order[i] = i
	}
	marked := map[string]bool{}
	for _, t := range in.Truncated {
		marked[t] = true
	}
	for size() > budget {
		sort.SliceStable(order, func(a, b int) bool { return len(in.Files[order[a]].Content) > len(in.Files[order[b]].Content) })
		f := &in.Files[order[0]]
		if len(f.Content) == 0 {
			break
		}
		if !marked[f.Path] {
			marked[f.Path] = true
			in.Truncated = append(in.Truncated, f.Path)
		}
		if len(f.Content) < 2048 {
			f.Content = ""
		} else {
			f.Content = f.Content[:len(f.Content)/2] + "\n[... corte de contexto ...]\n"
		}
	}
	return in
}

// Parse valida e decodifica a resposta.
func Parse(resp llm.Response) (Output, error) {
	var out Output
	if err := agents.Decode(OutSchema, resp, &out); err != nil {
		return Output{}, err
	}
	if out.Deepen && out.Category == "none" {
		return Output{}, fmt.Errorf("triager: deepen=true com category=none")
	}
	return out, nil
}

// Run executa uma triagem síncrona (uso pontual; a rodada usa batch via agents.RunAll).
func Run(ctx context.Context, client *llm.Client, in Input, o Options) (Output, error) {
	req, _, err := Request(in, o)
	if err != nil {
		return Output{}, err
	}
	resp, err := client.Call(ctx, req)
	if err != nil {
		return Output{}, err
	}
	return Parse(resp)
}
