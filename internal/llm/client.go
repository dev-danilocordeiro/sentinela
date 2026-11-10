// Package llm encapsula o SDK oficial da Anthropic para Go (github.com/anthropics/anthropic-sdk-go):
// chamada simples e em batch, structured outputs, prompt caching do contexto comum,
// contabilidade de custo e orçamento por rodada. Toda chamada é gravada via Recorder.
//
// Referências conferidas em 2026-10-09:
//   - Modelos e preços: https://platform.claude.com/docs/en/about-claude/models/overview
//     e https://platform.claude.com/docs/en/about-claude/pricing
//   - Structured outputs (output_config.format): https://platform.claude.com/docs/en/build-with-claude/structured-outputs
//   - Batch API: https://platform.claude.com/docs/en/build-with-claude/batch-processing
//   - Prompt caching: https://platform.claude.com/docs/en/build-with-claude/prompt-caching
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/dev-danilocordeiro/sentinela/internal/telemetry"
)

// Request é uma chamada de um agente. System é o contexto comum e recebe o
// breakpoint de cache; User é a parte variável.
type Request struct {
	Agent         string
	PromptVersion string
	Model         string
	System        string
	User          string
	MaxTokens     int64
	// Schema ativa structured outputs: a resposta é JSON válido contra ele.
	Schema map[string]any
	// Effort opcional: low | medium | high | xhigh | max.
	Effort string
}

// Response é o resultado de uma chamada bem-sucedida.
type Response struct {
	MessageID  string
	Model      string
	Text       string
	StopReason string
	Usage      Usage
	Cost       NanoUSD
	Latency    time.Duration
}

// DecodeJSON decodifica a saída estruturada em out, rejeitando campos desconhecidos.
func (r Response) DecodeJSON(out any) error {
	dec := json.NewDecoder(strings.NewReader(r.Text))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("saída do modelo não bate com o contrato: %w", err)
	}
	return nil
}

// CallRecord é uma linha de llm_calls.
type CallRecord struct {
	RunID         int64  // 0 quando a chamada não pertence a uma rodada
	Provider      string // anthropic_api | claude_code | replay
	Agent         string
	Model         string
	PromptVersion string
	Mode          string // sync | batch | cli
	BatchID       string
	CustomID      string
	MessageID     string
	Status        string // succeeded | errored | canceled | expired
	StopReason    string
	Usage         Usage
	Cost          NanoUSD
	Latency       time.Duration
	Error         string
}

// Recorder grava cada chamada de modelo (implementado por store.LLMRecorder).
type Recorder interface {
	RecordCall(ctx context.Context, rec CallRecord) error
}

// ErrBadStop indica que o modelo parou sem uma resposta utilizável (recusa ou max_tokens).
var ErrBadStop = errors.New("modelo parou sem resposta completa")

// Options configura um Client para uma rodada.
type Options struct {
	API          anthropic.Client
	Prices       *PriceTable
	Recorder     Recorder
	Budget       *Budget
	RunID        int64
	PollInterval time.Duration // intervalo de consulta de batches; padrão 30s
	Now          func() time.Time
	Tracer       trace.Tracer // padrão no-op
	// Sync força chamadas síncronas mesmo em Batch (models.use_batch: false).
	Sync bool
}

// Caller é o que os agentes usam para chamar modelos, independente do provider (ADR 0013).
type Caller interface {
	Call(ctx context.Context, req Request) (Response, error)
	// Batch devolve os resultados por CustomID; itens com problema trazem Err.
	Batch(ctx context.Context, items []BatchItem) (map[string]BatchResult, error)
	Budget() *Budget
	Provider() string
}

var _ Caller = (*Client)(nil)

// Provider identifica o provider em llm_calls.
func (c *Client) Provider() string { return "anthropic_api" }

// Client executa chamadas pela API da Anthropic respeitando o orçamento da rodada.
type Client struct {
	o Options
}

func NewClient(o Options) (*Client, error) {
	if o.Prices == nil || o.Recorder == nil || o.Budget == nil {
		return nil, errors.New("llm: Prices, Recorder e Budget são obrigatórios")
	}
	if o.PollInterval <= 0 {
		o.PollInterval = 30 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Tracer == nil {
		o.Tracer = noop.NewTracerProvider().Tracer(telemetry.TracerName)
	}
	return &Client{o: o}, nil
}

func (c *Client) Budget() *Budget { return c.o.Budget }

// Call faz uma chamada síncrona. Mesmo quando devolve ErrBudgetExceeded, a resposta
// é válida: o custo já foi incorrido e gravado; o chamador deve abortar a rodada.
func (c *Client) Call(ctx context.Context, req Request) (Response, error) {
	if err := c.o.Budget.Check(); err != nil {
		return Response{}, err
	}
	if err := validate(req); err != nil {
		return Response{}, err
	}
	if err := c.o.Budget.ReserveCall(); err != nil {
		return Response{}, err
	}
	ctx, span := c.o.Tracer.Start(ctx, "llm.call", trace.WithAttributes(
		telemetry.AttrAgent.String(req.Agent), telemetry.AttrModel.String(req.Model),
		telemetry.AttrPromptVersion.String(req.PromptVersion), telemetry.AttrMode.String("sync"),
	))
	defer span.End()
	start := c.o.Now()
	msg, apiErr := c.o.API.Messages.New(ctx, toParams(req))
	latency := c.o.Now().Sub(start)

	rec := CallRecord{
		RunID: c.o.RunID, Provider: c.Provider(), Agent: req.Agent, Model: req.Model, PromptVersion: req.PromptVersion,
		Mode: "sync", Latency: latency,
	}
	if apiErr != nil {
		rec.Status, rec.Error = "errored", apiErr.Error()
		span.SetStatus(codes.Error, rec.Error)
		if err := c.o.Recorder.RecordCall(ctx, rec); err != nil {
			return Response{}, errors.Join(apiErr, err)
		}
		return Response{}, fmt.Errorf("chamada %s: %w", req.Agent, apiErr)
	}
	resp, rec, err := c.account(ctx, rec, msg, false)
	annotate(span, rec)
	return resp, err
}

// annotate grava tokens, custo e desfecho da chamada no span.
func annotate(span trace.Span, rec CallRecord) {
	span.SetAttributes(
		telemetry.AttrModel.String(rec.Model),
		telemetry.AttrInputTokens.Int64(rec.Usage.InputTokens),
		telemetry.AttrOutputTokens.Int64(rec.Usage.OutputTokens),
		telemetry.AttrCacheRead.Int64(rec.Usage.CacheReadTokens),
		telemetry.AttrCacheWrite.Int64(rec.Usage.CacheWrite5mTokens+rec.Usage.CacheWrite1hTokens),
		telemetry.AttrCostUSD.Float64(rec.Cost.Dollars()),
		telemetry.AttrStopReason.String(rec.StopReason),
	)
	if rec.Error != "" {
		span.SetStatus(codes.Error, rec.Error)
	}
}

// account calcula custo, grava em llm_calls e debita do orçamento.
func (c *Client) account(ctx context.Context, rec CallRecord, msg *anthropic.Message, batch bool) (Response, CallRecord, error) {
	usage := usageOf(msg.Usage)
	model := msg.Model
	if model == "" {
		model = rec.Model
	}
	cost, err := c.o.Prices.Cost(model, usage, batch)
	if err != nil {
		return Response{}, rec, err
	}
	resp := Response{
		MessageID: msg.ID, Model: model, Text: textOf(msg), StopReason: string(msg.StopReason),
		Usage: usage, Cost: cost, Latency: rec.Latency,
	}
	rec.Model, rec.MessageID, rec.StopReason = model, msg.ID, resp.StopReason
	rec.Status, rec.Usage, rec.Cost = "succeeded", usage, cost

	var stopErr error
	if msg.StopReason == anthropic.StopReasonRefusal || msg.StopReason == anthropic.StopReasonMaxTokens {
		stopErr = fmt.Errorf("%w: stop_reason=%s", ErrBadStop, msg.StopReason)
		rec.Error = stopErr.Error()
	}
	if err := c.o.Recorder.RecordCall(ctx, rec); err != nil {
		return resp, rec, fmt.Errorf("gravando llm_call: %w", err)
	}
	if err := c.o.Budget.Charge(cost); err != nil {
		return resp, rec, err
	}
	return resp, rec, stopErr
}

func validate(req Request) error {
	switch {
	case req.Agent == "":
		return errors.New("llm: Agent é obrigatório")
	case req.PromptVersion == "":
		return errors.New("llm: PromptVersion é obrigatório")
	case req.Model == "":
		return errors.New("llm: Model é obrigatório")
	case req.MaxTokens <= 0:
		return errors.New("llm: MaxTokens deve ser positivo")
	}
	return nil
}

func toParams(req Request) anthropic.MessageNewParams {
	p := anthropic.MessageNewParams{
		Model:     req.Model,
		MaxTokens: req.MaxTokens,
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(req.User))},
	}
	if req.System != "" {
		// Contexto comum primeiro, com breakpoint de cache; a parte variável vai em User.
		p.System = []anthropic.TextBlockParam{{Text: req.System, CacheControl: anthropic.NewCacheControlEphemeralParam()}}
	}
	if req.Schema != nil {
		p.OutputConfig.Format = anthropic.JSONOutputFormatParam{Schema: req.Schema}
	}
	if req.Effort != "" {
		p.OutputConfig.Effort = anthropic.OutputConfigEffort(req.Effort)
	}
	return p
}

func usageOf(u anthropic.Usage) Usage {
	out := Usage{
		InputTokens:     u.InputTokens,
		OutputTokens:    u.OutputTokens,
		CacheReadTokens: u.CacheReadInputTokens,
	}
	out.CacheWrite5mTokens = u.CacheCreation.Ephemeral5mInputTokens
	out.CacheWrite1hTokens = u.CacheCreation.Ephemeral1hInputTokens
	// Quando a API não detalha por TTL, todo o cache_creation conta como 5 min.
	if detailed := out.CacheWrite5mTokens + out.CacheWrite1hTokens; detailed < u.CacheCreationInputTokens {
		out.CacheWrite5mTokens += u.CacheCreationInputTokens - detailed
	}
	return out
}

func textOf(msg *anthropic.Message) string {
	var b bytes.Buffer
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}
