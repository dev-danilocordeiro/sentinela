// Package signals define o contrato central Finding e a interface SignalSource.
// Todas as fontes (código, Sentry, Datadog) produzem o mesmo Finding, o que permite
// plugar fontes novas sem mexer no juiz, no publisher nem no executor.
package signals

import (
	"context"
	"crypto/sha1" //nolint:gosec // fingerprint de dedup definido pelo contrato, não é uso criptográfico
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/dev-danilocordeiro/sentinela/internal/repo"
)

// Fontes.
const (
	SourceCodeAnalysis = "code_analysis"
	SourceSentry       = "sentry"
	SourceDatadog      = "datadog"
	// SourceReviewNote: observação de um reviewer da linha de execução, fora do escopo do PR,
	// interpretada como achado candidato (ADR 0019).
	SourceReviewNote = "review_note"
)

var (
	Categories = []string{"bug", "performance", "improvement", "security"}
	Severities = []string{"critical", "high", "medium", "low"}
)

// Finding é o contrato entre fontes, juiz, publisher e executor (seção "Contrato central").
type Finding struct {
	Source             string       `json:"source"`
	Repo               string       `json:"repo"`
	Module             string       `json:"module"`
	Owners             []string     `json:"owners"`
	Routing            repo.Routing `json:"routing"`
	Category           string       `json:"category"`
	Severity           string       `json:"severity"`
	Confidence         float64      `json:"confidence"`
	Title              string       `json:"title"`
	Context            string       `json:"context"`
	Evidence           []Evidence   `json:"evidence"`
	Signals            []SignalRef  `json:"signals"`
	ProposedFix        string       `json:"proposed_fix"`
	AcceptanceCriteria []string     `json:"acceptance_criteria"`
	ScopePaths         []string     `json:"scope_paths"`
	// RelatedModules lista os outros módulos afetados por um achado que cruza módulos
	// (System Architect). Vazio para achados de um módulo só. Acrescentado na Fase 2 (ADR 0011).
	RelatedModules []string `json:"related_modules,omitempty"`
	Fingerprint    string   `json:"fingerprint"`
}

type Evidence struct {
	File      string `json:"file"`
	Line      int    `json:"line"`
	Permalink string `json:"permalink"`
	Snippet   string `json:"snippet"`
}

type SignalRef struct {
	Kind    string `json:"kind"` // scanner | sentry_event | dd_monitor | dd_trace | review_note
	Ref     string `json:"ref"`
	Summary string `json:"summary"`
}

// Validate checa as invariantes do contrato que o JSON Schema não expressa.
// CommandsScope são os caminhos que decidem o arquivo de comandos (AGENTS.md) do achado: o escopo
// da correção e, depois, os arquivos da evidência.
func (f Finding) CommandsScope() []string {
	scope := append([]string(nil), f.ScopePaths...)
	for _, e := range f.Evidence {
		if e.File != "" {
			scope = append(scope, e.File)
		}
	}
	return scope
}

func (f Finding) Validate() error {
	var errs []error
	if !slices.Contains([]string{SourceCodeAnalysis, SourceSentry, SourceDatadog, SourceReviewNote}, f.Source) {
		errs = append(errs, fmt.Errorf("source inválido %q", f.Source))
	}
	if !slices.Contains(Categories, f.Category) {
		errs = append(errs, fmt.Errorf("category inválida %q", f.Category))
	}
	if !slices.Contains(Severities, f.Severity) {
		errs = append(errs, fmt.Errorf("severity inválida %q", f.Severity))
	}
	if f.Confidence < 0 || f.Confidence > 1 {
		errs = append(errs, fmt.Errorf("confidence fora de 0..1: %v", f.Confidence))
	}
	if strings.TrimSpace(f.Title) == "" {
		errs = append(errs, errors.New("title vazio"))
	}
	if len(f.Evidence) == 0 {
		errs = append(errs, errors.New("sem evidência"))
	}
	for _, e := range f.Evidence {
		if e.File == "" || e.Line < 1 {
			errs = append(errs, fmt.Errorf("evidência inválida %s:%d", e.File, e.Line))
		}
	}
	if f.Repo == "" {
		errs = append(errs, errors.New("repo vazio"))
	}
	return errors.Join(errs...)
}

var nonWord = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// NormalizeProblem reduz um texto ao essencial para o fingerprint: minúsculas,
// sem pontuação nem números (linhas e contagens mudam entre rodadas).
func NormalizeProblem(s string) string {
	s = strings.ToLower(s)
	s = nonWord.ReplaceAllString(s, " ")
	words := strings.Fields(s)
	out := words[:0]
	for _, w := range words {
		if strings.IndexFunc(w, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
			continue
		}
		out = append(out, w)
	}
	return strings.Join(out, " ")
}

// Fingerprint = sha1(repo|category|file|problema-normalizado)[:12], conforme o contrato.
// file é o arquivo da evidência principal; problem é a chave curta do problema.
func Fingerprint(repoName, category, file, problem string) string {
	h := sha1.Sum([]byte(repoName + "|" + category + "|" + file + "|" + NormalizeProblem(problem))) //nolint:gosec // ver import
	return hex.EncodeToString(h[:])[:12]
}

// RawSignal é um sinal bruto de uma fonte, antes da interpretação.
type RawSignal struct {
	Source  string
	Ref     string // identificador estável na fonte (pacote, issue do Sentry, monitor)
	Repo    string
	Module  string
	Payload any
}

// SignalSource é implementada por cada fonte (code_analysis, sentry, datadog).
type SignalSource interface {
	Name() string
	Collect(ctx context.Context, since time.Time) ([]RawSignal, error)
	Interpret(ctx context.Context, s RawSignal) ([]Finding, error)
}

// BatchInterpreter é opcional: fontes que interpretam muitos sinais de uma vez
// (via Batch API) implementam isto, e o orquestrador prefere este caminho.
type BatchInterpreter interface {
	InterpretAll(ctx context.Context, s []RawSignal) ([]Finding, error)
}
