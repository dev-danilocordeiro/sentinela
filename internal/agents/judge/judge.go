// Package judge é o Judge: Sonnet, em batch, decide se cada achado é publicado,
// descartado ou mesclado com um achado existente.
package judge

import (
	"context"
	"fmt"
	"strings"

	"github.com/dev-danilocordeiro/sentinela/internal/agents"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
)

const Name = "judge"

var (
	prompt    = agents.MustPrompt(Name)
	inSchema  = agents.MustSchema(Name + ".input")
	OutSchema = agents.MustSchema(Name + ".output")
)

func PromptVersion() agents.Prompt { return prompt }

type Evidence struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Snippet string `json:"snippet"`
}

// Finding é o achado como o juiz o vê (sem roteamento nem permalinks).
type Finding struct {
	Source             string     `json:"source"`
	Repo               string     `json:"repo"`
	Module             string     `json:"module"`
	Category           string     `json:"category"`
	Severity           string     `json:"severity"`
	Confidence         float64    `json:"confidence"`
	Title              string     `json:"title"`
	Context            string     `json:"context"`
	Evidence           []Evidence `json:"evidence"`
	ProposedFix        string     `json:"proposed_fix"`
	AcceptanceCriteria []string   `json:"acceptance_criteria"`
	RelatedModules     []string   `json:"related_modules"`
	Fingerprint        string     `json:"fingerprint"`
}

type Similar struct {
	Fingerprint string  `json:"fingerprint"`
	Title       string  `json:"title"`
	Category    string  `json:"category"`
	State       string  `json:"state"`
	Similarity  float64 `json:"similarity"`
	// Issue é o identificador no Linear ("" se ainda não publicada).
	Issue string `json:"issue"`
	// AcceptanceCriteria e Evidence (arquivo:linha) do achado existente, para o Judge ver o
	// que ele já cobre.
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	Evidence           []string `json:"evidence"`
	// Overlap é o que o código detectou em comum com o achado novo.
	Overlap Overlap `json:"overlap"`
	// Location diz por que o código ligou este achado ao novo pelo lugar no código (mesmo
	// arquivo, símbolo ou linhas próximas, ADR 0025); vazio quando veio só pela similaridade.
	Location []string `json:"location"`
}

// Overlap lista os critérios de aceite do achado novo que parecem cobertos pelo existente e as
// evidências que apontam para o mesmo trecho.
type Overlap struct {
	Criteria []string `json:"criteria"`
	Evidence []string `json:"evidence"`
}

// Empty diz se não há sobreposição.
func (o Overlap) Empty() bool { return len(o.Criteria) == 0 && len(o.Evidence) == 0 }

type Example struct {
	Title    string `json:"title"`
	Category string `json:"category"`
	Outcome  string `json:"outcome"`
	Reason   string `json:"reason"`
}

type Input struct {
	Finding       Finding   `json:"finding"`
	Similar       []Similar `json:"similar"`
	Examples      []Example `json:"examples"`
	MinConfidence float64   `json:"min_confidence"`
}

// Decisões.
const (
	Publish = "publish"
	Discard = "discard"
	Merge   = "merge"
)

type Verdict struct {
	Decision   string  `json:"decision"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
	MergeWith  string  `json:"merge_with"`
	// ProductDecision é a resposta obrigatória a "este achado depende de uma decisão de
	// produto?", dada em todo veredito, inclusive descarte e mesclagem (ADR 0021).
	ProductDecision ProductDecision `json:"product_decision"`
	// Premise é a decisão de produto da qual o achado depende ("" se nenhuma), derivada de
	// ProductDecision por Parse; vai para o contexto e vira a pergunta ao time.
	Premise string `json:"premise,omitempty"`
	// RelatedTo são os fingerprints de achados abertos que cobrem parte deste; viram issues
	// relacionadas no Linear.
	RelatedTo []string `json:"related_to"`
	// Narrowed é o achado reduzido ao que os relacionados não cobrem (vazio se não reduziu).
	Narrowed Narrowed `json:"narrowed"`
}

// Narrowed é a versão do achado com escopo reduzido. Sem critérios, não houve redução.
type Narrowed struct {
	Title              string   `json:"title"`
	Context            string   `json:"context"`
	ProposedFix        string   `json:"proposed_fix"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
}

// Applied diz se o Judge reduziu o escopo.
func (n Narrowed) Applied() bool { return len(n.AcceptanceCriteria) > 0 }

// ProductDecision: o achado depende de uma decisão de produto e qual.
type ProductDecision struct {
	Depends  bool   `json:"depends"`
	Decision string `json:"decision"`
}

// ApplyPremise registra a premissa de produto no contexto do achado.
func (v Verdict) ApplyPremise(context string) string {
	p := strings.TrimSpace(v.Premise)
	if p == "" {
		return context
	}
	return strings.TrimSpace(context + "\n\nPremissa de produto: " + p)
}

// Publishable aplica o limiar da casa: publicar abaixo de min_confidence vale como descarte.
func (v Verdict) Publishable(minConfidence float64) bool {
	return v.Decision == Publish && v.Confidence >= minConfidence
}

type Options struct {
	Model string
}

const MaxTokens = 4000

func Request(in Input, o Options) (llm.Request, error) {
	in.Similar, in.Examples = agents.Slice(in.Similar), agents.Slice(in.Examples)
	for i := range in.Similar {
		sm := &in.Similar[i]
		sm.AcceptanceCriteria, sm.Evidence = agents.Slice(sm.AcceptanceCriteria), agents.Slice(sm.Evidence)
		sm.Overlap.Criteria, sm.Overlap.Evidence = agents.Slice(sm.Overlap.Criteria), agents.Slice(sm.Overlap.Evidence)
		sm.Location = agents.Slice(sm.Location)
	}
	in.Finding.Evidence, in.Finding.AcceptanceCriteria = agents.Slice(in.Finding.Evidence), agents.Slice(in.Finding.AcceptanceCriteria)
	in.Finding.RelatedModules = agents.Slice(in.Finding.RelatedModules)
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

func Parse(resp llm.Response) (Verdict, error) {
	var v Verdict
	if err := agents.Decode(OutSchema, resp, &v); err != nil {
		return Verdict{}, err
	}
	if v.Confidence < 0 || v.Confidence > 1 {
		return Verdict{}, fmt.Errorf("judge: confidence %v fora de 0..1", v.Confidence)
	}
	if v.Decision == Merge && v.MergeWith == "" {
		return Verdict{}, fmt.Errorf("judge: merge sem merge_with")
	}
	pd := &v.ProductDecision
	pd.Decision = strings.TrimSpace(pd.Decision)
	switch {
	case pd.Depends && pd.Decision == "":
		return Verdict{}, fmt.Errorf("judge: product_decision.depends sem dizer qual decisão")
	case !pd.Depends:
		pd.Decision = ""
	}
	v.Premise = pd.Decision
	if v.Narrowed.Applied() && len(v.RelatedTo) == 0 {
		return Verdict{}, fmt.Errorf("judge: escopo reduzido sem related_to")
	}
	return v, nil
}

func Run(ctx context.Context, client *llm.Client, in Input, o Options) (Verdict, error) {
	req, err := Request(in, o)
	if err != nil {
		return Verdict{}, err
	}
	resp, err := client.Call(ctx, req)
	if err != nil {
		return Verdict{}, err
	}
	return Parse(resp)
}
