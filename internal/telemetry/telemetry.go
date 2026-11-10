// Package telemetry configura o OpenTelemetry do próprio Sentinela: um trace por rodada,
// com spans por repo, por agente e por chamada de modelo. Sem endpoint OTLP configurado,
// o provider é no-op e nada sai do processo.
package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
)

// TracerName identifica a instrumentação do Sentinela.
const TracerName = "github.com/dev-danilocordeiro/sentinela"

// Atributos usados nos spans (prefixo sentinela.* para não colidir com semconv).
const (
	AttrRunID         = attribute.Key("sentinela.run_id")
	AttrRepo          = attribute.Key("sentinela.repo")
	AttrAgent         = attribute.Key("sentinela.agent")
	AttrModel         = attribute.Key("sentinela.model")
	AttrPromptVersion = attribute.Key("sentinela.prompt_version")
	AttrMode          = attribute.Key("sentinela.mode")
	AttrInputTokens   = attribute.Key("sentinela.tokens.input")
	AttrOutputTokens  = attribute.Key("sentinela.tokens.output")
	AttrCacheRead     = attribute.Key("sentinela.tokens.cache_read")
	AttrCacheWrite    = attribute.Key("sentinela.tokens.cache_write")
	AttrCostUSD       = attribute.Key("sentinela.cost_usd")
	AttrItems         = attribute.Key("sentinela.items")
	AttrBatchID       = attribute.Key("sentinela.batch_id")
	AttrCustomID      = attribute.Key("sentinela.custom_id")
	AttrStopReason    = attribute.Key("sentinela.stop_reason")
)

// Setup devolve um TracerProvider e a função de shutdown. Endpoint vazio = no-op.
func Setup(ctx context.Context, cfg config.Telemetry) (trace.TracerProvider, func(context.Context) error, error) {
	if cfg.OTLPEndpoint == "" {
		return noop.NewTracerProvider(), func(context.Context) error { return nil }, nil
	}
	exp, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(cfg.OTLPEndpoint))
	if err != nil {
		return nil, nil, fmt.Errorf("criando exporter OTLP: %w", err)
	}
	res := resource.NewSchemaless(semconv.ServiceName(cfg.ServiceName))
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	return tp, tp.Shutdown, nil
}
