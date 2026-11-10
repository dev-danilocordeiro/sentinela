package llm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/dev-danilocordeiro/sentinela/internal/telemetry"
)

// BatchItem é uma requisição dentro de um batch, identificada por CustomID.
type BatchItem struct {
	CustomID string
	Request  Request
}

// BatchResult é o resultado de um item. Err != nil quando o item falhou.
type BatchResult struct {
	Response
	Err error
}

// Batch envia os itens pela Message Batches API (50% do preço), espera o fim do
// processamento e devolve os resultados por CustomID (a API não garante ordem).
//
// Antes de enviar, estima o pior caso (entrada aproximada + MaxTokens de saída) e
// recusa o batch se ele não couber no que resta do orçamento.
func (c *Client) Batch(ctx context.Context, items []BatchItem) (map[string]BatchResult, error) {
	if len(items) == 0 {
		return map[string]BatchResult{}, nil
	}
	if c.o.Sync {
		return c.batchSync(ctx, items)
	}
	if err := c.o.Budget.Check(); err != nil {
		return nil, err
	}
	reqs := make([]anthropic.MessageBatchNewParamsRequest, 0, len(items))
	byID := make(map[string]Request, len(items))
	var worst NanoUSD
	for _, it := range items {
		if err := validate(it.Request); err != nil {
			return nil, fmt.Errorf("item %s: %w", it.CustomID, err)
		}
		if _, dup := byID[it.CustomID]; dup || it.CustomID == "" {
			return nil, fmt.Errorf("custom_id vazio ou repetido: %q", it.CustomID)
		}
		byID[it.CustomID] = it.Request
		est, err := c.o.Prices.Cost(it.Request.Model, worstCaseUsage(it.Request), true)
		if err != nil {
			return nil, err
		}
		worst += est
		reqs = append(reqs, anthropic.MessageBatchNewParamsRequest{CustomID: it.CustomID, Params: toBatchParams(it.Request)})
	}
	if remaining := c.o.Budget.Remaining(); worst > remaining {
		return nil, fmt.Errorf("%w: batch pode custar até %s, restam %s", ErrBudgetExceeded, worst, remaining)
	}
	for range items {
		if err := c.o.Budget.ReserveCall(); err != nil {
			return nil, err
		}
	}

	ctx, span := c.o.Tracer.Start(ctx, "llm.batch", trace.WithAttributes(telemetry.AttrItems.Int(len(items))))
	defer span.End()
	start := c.o.Now()
	batch, err := c.o.API.Messages.Batches.New(ctx, anthropic.MessageBatchNewParams{Requests: reqs})
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("criando batch: %w", err)
	}
	span.SetAttributes(telemetry.AttrBatchID.String(batch.ID))
	if err := c.waitBatch(ctx, batch.ID); err != nil {
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	end := c.o.Now()
	latency := end.Sub(start)
	// Um span por item, com o intervalo do batch: o batch não informa o tempo de cada item.
	itemSpan := func(rec CallRecord) {
		_, s := c.o.Tracer.Start(ctx, "llm.call", trace.WithTimestamp(start), trace.WithAttributes(
			telemetry.AttrAgent.String(rec.Agent), telemetry.AttrPromptVersion.String(rec.PromptVersion),
			telemetry.AttrMode.String("batch"), telemetry.AttrBatchID.String(rec.BatchID), telemetry.AttrCustomID.String(rec.CustomID),
		))
		annotate(s, rec)
		s.End(trace.WithTimestamp(end))
	}

	results := make(map[string]BatchResult, len(items))
	var budgetErr error
	stream := c.o.API.Messages.Batches.ResultsStreaming(ctx, batch.ID, anthropic.MessageBatchResultsParams{})
	defer func() { _ = stream.Close() }()
	for stream.Next() {
		item := stream.Current()
		req, ok := byID[item.CustomID]
		if !ok {
			continue
		}
		rec := CallRecord{
			RunID: c.o.RunID, Provider: c.Provider(), Agent: req.Agent, Model: req.Model, PromptVersion: req.PromptVersion,
			Mode: "batch", BatchID: batch.ID, CustomID: item.CustomID, Latency: latency,
		}
		if item.Result.Type != "succeeded" {
			rec.Status = item.Result.Type
			rec.Error = item.Result.Error.Error.Message
			itemErr := fmt.Errorf("item %s: %s %s", item.CustomID, item.Result.Type, rec.Error)
			if err := c.o.Recorder.RecordCall(ctx, rec); err != nil {
				return results, fmt.Errorf("gravando llm_call: %w", err)
			}
			itemSpan(rec)
			results[item.CustomID] = BatchResult{Err: itemErr}
			continue
		}
		msg := item.Result.Message
		resp, rec, err := c.account(ctx, rec, &msg, true)
		itemSpan(rec)
		switch {
		case errors.Is(err, ErrBudgetExceeded):
			budgetErr = err
			results[item.CustomID] = BatchResult{Response: resp}
		case err != nil && !errors.Is(err, ErrBadStop):
			return results, err
		default:
			results[item.CustomID] = BatchResult{Response: resp, Err: err}
		}
	}
	if err := stream.Err(); err != nil {
		return results, fmt.Errorf("lendo resultados do batch %s: %w", batch.ID, err)
	}
	for id := range byID {
		if _, ok := results[id]; !ok {
			results[id] = BatchResult{Err: fmt.Errorf("item %s ausente nos resultados", id)}
		}
	}
	return results, budgetErr
}

func (c *Client) waitBatch(ctx context.Context, id string) error {
	t := time.NewTicker(c.o.PollInterval)
	defer t.Stop()
	for {
		b, err := c.o.API.Messages.Batches.Get(ctx, id, anthropic.MessageBatchGetParams{})
		if err != nil {
			return fmt.Errorf("consultando batch %s: %w", id, err)
		}
		if b.ProcessingStatus == anthropic.MessageBatchProcessingStatusEnded {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// worstCaseUsage estima o maior custo possível de uma requisição: entrada contada
// por byte/3 (acima de qualquer tokenizer prático) e saída igual a MaxTokens.
func worstCaseUsage(req Request) Usage {
	in := int64(len(req.System)+len(req.User))/3 + 1
	return Usage{InputTokens: in, OutputTokens: req.MaxTokens}
}

func toBatchParams(req Request) anthropic.MessageBatchNewParamsRequestParams {
	p := toParams(req)
	return anthropic.MessageBatchNewParamsRequestParams{
		Model:        p.Model,
		MaxTokens:    p.MaxTokens,
		Messages:     p.Messages,
		System:       p.System,
		OutputConfig: p.OutputConfig,
	}
}

// batchSync executa os itens um a um pela Messages API (models.use_batch: false),
// com a mesma semântica de resultado do Batch.
func (c *Client) batchSync(ctx context.Context, items []BatchItem) (map[string]BatchResult, error) {
	results := make(map[string]BatchResult, len(items))
	for _, it := range items {
		if _, dup := results[it.CustomID]; dup || it.CustomID == "" {
			return nil, fmt.Errorf("custom_id vazio ou repetido: %q", it.CustomID)
		}
		resp, err := c.Call(ctx, it.Request)
		switch {
		case errors.Is(err, ErrBudgetExceeded):
			results[it.CustomID] = BatchResult{Response: resp}
			return results, err
		default:
			results[it.CustomID] = BatchResult{Response: resp, Err: err}
		}
	}
	return results, nil
}
