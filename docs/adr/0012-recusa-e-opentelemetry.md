# ADR 0012: Fallback em recusa e OpenTelemetry

- Status: aceita
- Data: 2026-10-09

## Recusa do modelo
- `models.refusal.action`: `fallback_model` (com `fallback_model`) ou `skip`.
- Implementado no cliente (`agents.RunAll`): itens com `stop_reason=refusal` são repetidos num
  segundo lote com o modelo de fallback. Se recusar de novo, ou com `skip`, o item é pulado com o
  motivo gravado em `run_items`. Resposta cortada em `max_tokens` e erro de item também viram pulo
  com motivo. A rodada só cai por orçamento ou por erro da API no envio do lote.
- O `fallbacks` server-side da API ficou de fora: ele não vale para o Haiku 5.5, varia por
  plataforma e entra só como beta. A versão no cliente funciona igual para os três modelos e em
  Batch, e cada tentativa fica em `llm_calls`.

## OpenTelemetry
- Um trace por rodada: `sentinela.run` → `repo <nome>` → `agent.<nome>` → `llm.batch` →
  `llm.call` (um por item, com o intervalo do batch, que não informa o tempo de cada item). Chamadas
  síncronas geram `llm.call` direto.
- Atributos `sentinela.*`: run_id, repo, agente, modelo, versão do prompt, modo, batch_id,
  custom_id, tokens (entrada, saída, cache lido e escrito), custo em USD e stop_reason.
- Exporter OTLP/HTTP (`go.opentelemetry.io/otel` v1.47) em `telemetry.otlp_endpoint`. Vazio =
  provider no-op, nada sai do processo (padrão).
