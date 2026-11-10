// Package agents tem o que é comum aos agentes LLM: prompts versionados, schemas
// dos contratos, validação, estimativa de contexto e execução em lote com política
// de recusa. Cada agente fica em internal/agents/<nome>.
package agents

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.opentelemetry.io/otel/trace"
	"gopkg.in/yaml.v3"

	"github.com/dev-danilocordeiro/sentinela"
	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
	"github.com/dev-danilocordeiro/sentinela/internal/telemetry"
)

// Prompt é um prompt versionado de prompts/<agente>.md.
type Prompt struct {
	Agent   string
	Version string
	Body    string
	SHA256  string
}

// LoadPrompt lê o prompt e o frontmatter (agent, version).
func LoadPrompt(agent string) (Prompt, error) {
	data, err := sentinela.Prompts.ReadFile("prompts/" + agent + ".md")
	if err != nil {
		return Prompt{}, fmt.Errorf("prompt de %s: %w", agent, err)
	}
	text := string(data)
	if !strings.HasPrefix(text, "---\n") {
		return Prompt{}, fmt.Errorf("prompt de %s sem frontmatter", agent)
	}
	front, body, ok := strings.Cut(text[4:], "\n---\n")
	if !ok {
		return Prompt{}, fmt.Errorf("prompt de %s: frontmatter sem fechamento", agent)
	}
	var meta struct {
		Agent   string `yaml:"agent"`
		Version string `yaml:"version"`
	}
	if err := yaml.Unmarshal([]byte(front), &meta); err != nil {
		return Prompt{}, fmt.Errorf("frontmatter de %s: %w", agent, err)
	}
	if meta.Agent != agent || meta.Version == "" {
		return Prompt{}, fmt.Errorf("frontmatter de %s: agent=%q version=%q", agent, meta.Agent, meta.Version)
	}
	sum := sha256.Sum256(data)
	return Prompt{Agent: agent, Version: agent + "@" + meta.Version, Body: strings.TrimSpace(body), SHA256: hex.EncodeToString(sum[:])}, nil
}

// MustPrompt é LoadPrompt para inicialização de pacote: os prompts são embutidos e
// testados, então um erro aqui é bug de build.
func MustPrompt(agent string) Prompt {
	p, err := LoadPrompt(agent)
	if err != nil {
		panic(err)
	}
	return p
}

// Schema é um JSON Schema de contrato, compilado para validação.
type Schema struct {
	Name     string
	Raw      map[string]any // enviado à API em output_config.format
	compiled *jsonschema.Schema
}

var (
	schemaMu    sync.Mutex
	schemaCache = map[string]*Schema{}
)

// LoadSchema carrega schemas/<nome>.json (ex.: "triager.output").
func LoadSchema(name string) (*Schema, error) {
	schemaMu.Lock()
	defer schemaMu.Unlock()
	if s, ok := schemaCache[name]; ok {
		return s, nil
	}
	data, err := sentinela.Schemas.ReadFile("schemas/" + name + ".json")
	if err != nil {
		return nil, fmt.Errorf("schema %s: %w", name, err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("schema %s: %w", name, err)
	}
	c := jsonschema.NewCompiler()
	url := "sentinela://schemas/" + name + ".json"
	if err := c.AddResource(url, doc); err != nil {
		return nil, err
	}
	compiled, err := c.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("compilando schema %s: %w", name, err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	// $schema e title são metadados do arquivo, não fazem parte do formato pedido à API.
	delete(raw, "$schema")
	delete(raw, "title")
	s := &Schema{Name: name, Raw: raw, compiled: compiled}
	schemaCache[name] = s
	return s, nil
}

// MustSchema é LoadSchema para inicialização de pacote.
func MustSchema(name string) *Schema {
	s, err := LoadSchema(name)
	if err != nil {
		panic(err)
	}
	return s
}

// Validate valida um valor Go (serializado em JSON) contra o schema.
func (s *Schema) Validate(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.ValidateJSON(data)
}

// ValidateJSON valida um documento JSON contra o schema.
func (s *Schema) ValidateJSON(data []byte) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("%s: JSON inválido: %w", s.Name, err)
	}
	if err := s.compiled.Validate(doc); err != nil {
		return fmt.Errorf("%s: %w", s.Name, err)
	}
	return nil
}

// Decode valida a saída do modelo contra o schema e decodifica em out.
func Decode(s *Schema, resp llm.Response, out any) error {
	if err := s.ValidateJSON([]byte(resp.Text)); err != nil {
		return err
	}
	return resp.DecodeJSON(out)
}

// EncodeInput valida a entrada contra o schema e devolve o JSON enviado como mensagem do usuário.
func EncodeInput(s *Schema, in any) (string, error) {
	if err := s.Validate(in); err != nil {
		return "", err
	}
	data, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// BytesPerToken é a razão conservadora usada para estimar tokens sem chamar a API.
// Código costuma render 3 a 4 bytes por token; 2 deixa folga para o tokenizer novo
// (que conta ~30% mais tokens) e para JSON com código escapado (ADR 0009). O custo real
// de cada chamada fica em llm_calls e a rodada avisa se algum prompt do Triager passar de 100K.
const BytesPerToken = 2.0

// EstimateTokens estima, por cima, quantos tokens um texto ocupa.
func EstimateTokens(s string) int {
	return int(float64(len(s))/BytesPerToken) + 1
}

// NumberLines prefixa cada linha com o número dela, para o modelo citar arquivo:linha.
func NumberLines(content string) string {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	var b strings.Builder
	b.Grow(len(content) + len(lines)*6)
	for i, l := range lines {
		fmt.Fprintf(&b, "%d| %s\n", i+1, l)
	}
	return b.String()
}

// ItemLog registra o desfecho de cada item processado por um agente (tabela run_items).
type ItemLog interface {
	Item(ctx context.Context, agent, key, status, model string, output any, reason string) error
}

// Item é uma requisição de agente identificada por uma chave estável (pacote, fingerprint).
type Item struct {
	Key     string
	Request llm.Request
	// Fallback é o modelo para repetir o item se o modelo dele recusar; vazio usa o da política.
	// Vem de config.ModelsFor, para respeitar o repo e o time do item.
	Fallback string
}

// Outcome é o desfecho de um item: resposta utilizável ou pulado com motivo.
type Outcome struct {
	Key      string
	Response llm.Response
	Skipped  bool
	Reason   string
}

// RunAll executa os itens em lote. Recusa do modelo segue a política configurada:
// fallback_model repete os itens recusados com outro modelo; skip pula com motivo.
// Respostas cortadas, erros de item e recusas finais viram itens pulados; só falhas
// da rodada (orçamento, API indisponível) devolvem erro.
func RunAll(ctx context.Context, client llm.Caller, tracer trace.Tracer, agent string, items []Item, policy config.RefusalPolicy) (map[string]Outcome, error) {
	if len(items) == 0 {
		return map[string]Outcome{}, nil
	}
	ctx, span := tracer.Start(ctx, "agent."+agent, trace.WithAttributes(
		telemetry.AttrAgent.String(agent), telemetry.AttrItems.Int(len(items)),
	))
	defer span.End()

	outcomes := make(map[string]Outcome, len(items))
	batchItems := make([]llm.BatchItem, 0, len(items))
	byKey := make(map[string]llm.Request, len(items))
	fallback := make(map[string]string, len(items))
	for _, it := range items {
		batchItems = append(batchItems, llm.BatchItem{CustomID: customID(it.Key), Request: it.Request})
		byKey[customID(it.Key)] = it.Request
		fallback[customID(it.Key)] = it.Fallback
		if it.Fallback == "" {
			fallback[customID(it.Key)] = policy.FallbackModel
		}
	}
	keyOf := make(map[string]string, len(items))
	for _, it := range items {
		keyOf[customID(it.Key)] = it.Key
	}

	results, err := client.Batch(ctx, batchItems)
	if err != nil && !errors.Is(err, llm.ErrBudgetExceeded) {
		return nil, err
	}
	budgetErr := err

	var retry []llm.BatchItem
	for id, r := range results {
		key := keyOf[id]
		switch {
		case r.Err == nil:
			outcomes[key] = Outcome{Key: key, Response: r.Response}
		case r.StopReason == "refusal" && policy.Action == config.RefusalFallbackModel &&
			fallback[id] != "" && fallback[id] != byKey[id].Model:
			req := byKey[id]
			req.Model = fallback[id]
			retry = append(retry, llm.BatchItem{CustomID: id, Request: req})
		case r.StopReason == "refusal":
			outcomes[key] = Outcome{Key: key, Response: r.Response, Skipped: true, Reason: "recusa do modelo " + r.Model}
		case r.StopReason == "max_tokens":
			outcomes[key] = Outcome{Key: key, Response: r.Response, Skipped: true, Reason: "resposta cortada em max_tokens"}
		default:
			outcomes[key] = Outcome{Key: key, Skipped: true, Reason: r.Err.Error()}
		}
	}
	if len(retry) > 0 && budgetErr == nil {
		second, err := client.Batch(ctx, retry)
		if err != nil && !errors.Is(err, llm.ErrBudgetExceeded) {
			return nil, err
		}
		budgetErr = err
		for _, it := range retry {
			key := keyOf[it.CustomID]
			r, ok := second[it.CustomID]
			switch {
			case ok && r.Err == nil:
				outcomes[key] = Outcome{Key: key, Response: r.Response}
			case ok && r.StopReason == "refusal":
				outcomes[key] = Outcome{Key: key, Response: r.Response, Skipped: true,
					Reason: "recusa do modelo " + byKey[it.CustomID].Model + " e do fallback " + it.Request.Model}
			case ok && r.Err != nil:
				outcomes[key] = Outcome{Key: key, Skipped: true, Reason: "fallback " + it.Request.Model + ": " + r.Err.Error()}
			default:
				outcomes[key] = Outcome{Key: key, Skipped: true, Reason: "fallback não executado"}
			}
		}
	}
	for _, it := range items {
		if _, ok := outcomes[it.Key]; !ok {
			outcomes[it.Key] = Outcome{Key: it.Key, Skipped: true, Reason: "não executado: orçamento da rodada"}
		}
	}
	return outcomes, budgetErr
}

// customID adapta a chave ao formato aceito pela Batch API (letras, números, _ e -, até 64).
func customID(key string) string {
	var b strings.Builder
	for _, r := range key {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	sum := sha256.Sum256([]byte(key))
	s := b.String()
	if len(s) > 48 {
		s = s[len(s)-48:]
	}
	return s + "-" + hex.EncodeToString(sum[:])[:8]
}

// EncodeInputUnchecked serializa a entrada sem validar (usado para medir tamanho).
func EncodeInputUnchecked(in any) (string, error) {
	data, err := json.Marshal(in)
	return string(data), err
}

// Slice devolve s ou uma fatia vazia: o JSON precisa de [] e não de null nos contratos.
func Slice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// File é um arquivo do pacote enviado ao modelo, com linhas numeradas.
type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Signal é um sinal de scanner como o modelo o vê.
type Signal struct {
	Ref      string `json:"ref"`
	Scanner  string `json:"scanner"`
	Rule     string `json:"rule"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// Suppression é um problema já recusado por humanos.
type Suppression struct {
	Fingerprint string `json:"fingerprint"`
	Pattern     string `json:"pattern"`
	Category    string `json:"category"`
	Reason      string `json:"reason"`
}

// FindingOut é o achado como os modelos o devolvem (Analyst e Architect).
type FindingOut struct {
	Category           string     `json:"category"`
	Severity           string     `json:"severity"`
	Confidence         float64    `json:"confidence"`
	Title              string     `json:"title"`
	Context            string     `json:"context"`
	ProblemKey         string     `json:"problem_key"`
	Evidence           []Evidence `json:"evidence"`
	ProposedFix        string     `json:"proposed_fix"`
	AcceptanceCriteria []string   `json:"acceptance_criteria"`
	ScopePaths         []string   `json:"scope_paths"`
	SignalRefs         []string   `json:"signal_refs,omitempty"`
	AffectedModules    []string   `json:"affected_modules,omitempty"`
}

type Evidence struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Snippet string `json:"snippet"`
}
