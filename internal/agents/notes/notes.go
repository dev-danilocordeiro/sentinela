// Package notes é o Review Note Interpreter: transforma as observações fora do escopo dos
// reviewers da linha de execução em achados candidatos (source review_note), que seguem para a
// validação de evidência, a deduplicação e o Judge como qualquer achado (ADR 0019).
package notes

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/dev-danilocordeiro/sentinela/internal/agents"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
)

const Name = "notes"

var (
	prompt    = agents.MustPrompt(Name)
	inSchema  = agents.MustSchema(Name + ".input")
	OutSchema = agents.MustSchema(Name + ".output")
)

func PromptVersion() agents.Prompt { return prompt }

// Execution é a issue que o PR resolveu, para o intérprete não repetir o que já foi feito.
type Execution struct {
	Issue       string   `json:"issue"`
	Title       string   `json:"title"`
	PRURL       string   `json:"pr_url"`
	HeadSHA     string   `json:"head_sha"`
	ScopePaths  []string `json:"scope_paths"`
	ProposedFix string   `json:"proposed_fix"`
}

// Note é uma observação de um reviewer.
type Note struct {
	ID    string `json:"id"`
	Lens  string `json:"lens"`
	Round int    `json:"round"`
	Text  string `json:"text"`
}

// Excerpt é um trecho real do código em volta das linhas que as notas citam.
type Excerpt struct {
	File      string `json:"file"`
	StartLine int    `json:"start_line"`
	Code      string `json:"code"`
}

type Input struct {
	Repo        string    `json:"repo"`
	Execution   Execution `json:"execution"`
	Notes       []Note    `json:"notes"`
	Excerpts    []Excerpt `json:"excerpts"`
	MaxFindings int       `json:"max_findings"`
}

type Output struct {
	Findings []agents.FindingOut `json:"findings"`
	Ignored  []Ignored           `json:"ignored_notes"`
}

// Ignored é uma nota que não virou achado, com o motivo do intérprete.
type Ignored struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

type Options struct {
	Model string
}

const MaxTokens = 16000

func Request(in Input, o Options) (llm.Request, error) {
	in.Notes, in.Excerpts = agents.Slice(in.Notes), agents.Slice(in.Excerpts)
	in.Execution.ScopePaths = agents.Slice(in.Execution.ScopePaths)
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

// Parse valida a saída: cada achado precisa citar ao menos uma nota existente; ids desconhecidos
// saem; achados além do limite são descartados.
func Parse(resp llm.Response, in Input) (Output, []string, error) {
	var out Output
	if err := agents.Decode(OutSchema, resp, &out); err != nil {
		return Output{}, nil, err
	}
	known := map[string]bool{}
	for _, n := range in.Notes {
		known[n.ID] = true
	}
	var kept []agents.FindingOut
	var notes []string
	for i, f := range out.Findings {
		if f.Confidence < 0 || f.Confidence > 1 {
			return Output{}, nil, fmt.Errorf("notes: achado %d com confidence %v fora de 0..1", i, f.Confidence)
		}
		var refs []string
		for _, r := range f.SignalRefs {
			if known[r] {
				refs = append(refs, r)
			}
		}
		if len(refs) == 0 {
			notes = append(notes, fmt.Sprintf("achado %q descartado: não cita nenhuma nota da entrada (%v)", f.Title, f.SignalRefs))
			continue
		}
		f.SignalRefs = refs
		kept = append(kept, f)
	}
	covered := map[string]bool{}
	for _, f := range kept {
		for _, r := range f.SignalRefs {
			covered[r] = true
		}
	}
	var ignored []Ignored
	for _, ig := range out.Ignored {
		if known[ig.ID] && !covered[ig.ID] {
			ignored = append(ignored, ig)
			covered[ig.ID] = true
		}
	}
	for _, n := range in.Notes {
		if !covered[n.ID] {
			ignored = append(ignored, Ignored{ID: n.ID, Reason: "o intérprete não disse o que fez com esta nota"})
		}
	}
	out.Ignored = agents.Slice(ignored)
	if in.MaxFindings > 0 && len(kept) > in.MaxFindings {
		notes = append(notes, fmt.Sprintf("%d achado(s) além do limite descartados", len(kept)-in.MaxFindings))
		kept = kept[:in.MaxFindings]
	}
	out.Findings = agents.Slice(kept)
	return out, notes, nil
}

// Prefixos com que os reviewers marcam o que não bloqueia o PR.
var notePrefixes = []string{"(fora do escopo)", "(não bloqueia)"}

// IsNote diz se um item de review é uma observação que não bloqueia (candidata a achado) e
// devolve o texto sem o prefixo.
func IsNote(item string) (string, bool) {
	t := strings.TrimSpace(item)
	for _, p := range notePrefixes {
		if strings.HasPrefix(strings.ToLower(t), p) {
			return strings.TrimSpace(t[len(p):]), true
		}
	}
	return "", false
}

// fileRef casa "caminho/arquivo.ext:12", "arquivo.ext:12-30" e "arquivo.ext:38,40".
var fileRef = regexp.MustCompile("`?([A-Za-z0-9_./-]+\\.[A-Za-z0-9]+)`?:(\\d+)(?:[-,](\\d+))?")

// Ref é uma referência arquivo:linha numa nota.
type Ref struct {
	File       string
	Start, End int
}

// Refs extrai as referências arquivo:linha de uma nota.
func Refs(text string) []Ref {
	var out []Ref
	for _, m := range fileRef.FindAllStringSubmatch(text, -1) {
		var start, end int
		_, _ = fmt.Sscanf(m[2], "%d", &start)
		end = start
		if m[3] != "" {
			_, _ = fmt.Sscanf(m[3], "%d", &end)
			if end < start {
				end = start
			}
		}
		out = append(out, Ref{File: m[1], Start: start, End: end})
	}
	return out
}
