# 0001: OpenTelemetry traces for every run, off by default

- Status: accepted
- Detailed record: [ADR 0012](../adr/0012-recusa-e-opentelemetry.md)

## Context

A discovery run fans out into many model calls. They cover several repos and agents (Triager,
Analyst, Architect, Judge), most of them inside Batch API jobs. When a run is slow or expensive,
the question is always "which repo, which agent, which call". Logs alone cannot answer that
without stitching IDs by hand.

## Decision

- **One trace per run:** `sentinela.run` → `repo <name>` → `agent.<name>` → `llm.batch` →
  `llm.call`. A synchronous call produces `llm.call` directly.
- **Batch item spans:** they carry the batch window, because the Batch API does not report
  per-item timing.
- **Attributes (`sentinela.*`):** run id, repo, agent, model, prompt version, mode, batch id,
  custom id, tokens (input, output, cache read and write), cost in USD and stop reason.
- **Exporter:** OTLP over HTTP (`go.opentelemetry.io/otel`) to `telemetry.otlp_endpoint`. An
  empty endpoint means a no-op provider, and nothing leaves the process. This is the default.
- **Source of truth:** the same numbers are always written to the `llm_calls` table. Tracing is
  for navigation; the database is for accounting and budgets.

## Consequences

- Any OTLP backend (Jaeger, Tempo, Honeycomb, Datadog) shows a run as one tree, with cost per
  branch.
- A local install sends no telemetry unless someone configures an endpoint.
- The per-item duration inside a batch is approximate.
