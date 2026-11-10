# ADR 0003: Structured outputs via output_config.format em vez de tool use forçado

- Status: aceita
- Data: 2026-10-09

## Contexto
O princípio 2 pede JSON validado contra schema via "structured output via tool use". Na documentação
atual, `tool_choice` forçado (`any`/`tool`) retorna 400 em `claude-opus-5-5` e `claude-sonnet-5-5`,
inclusive em Batches. A forma recomendada é structured outputs: `output_config.format` com
`{"type": "json_schema", "schema": ...}` (platform.claude.com/docs/en/build-with-claude/structured-outputs).
O SDK em Go expõe isso como `OutputConfig.Format = anthropic.JSONOutputFormatParam{Schema: ...}`.

## Decisão
- `llm.Request.Schema` ativa structured outputs; a API garante JSON válido contra o schema.
- O lado Go decodifica com `DisallowUnknownFields` (`Response.DecodeJSON`), então um contrato
  desatualizado falha alto em vez de perder campos.
- `stop_reason` igual a `refusal` ou `max_tokens` vira `llm.ErrBadStop`; a chamada é gravada com custo.
- O parâmetro `fallbacks` (server-side fallback em recusa) fica para a Fase 2, quando houver agentes reais.

## Consequências
O espírito do princípio 2 (contratos tipados) se mantém, mas o mecanismo muda. Os schemas JSON
ficam em `schemas/` a partir da Fase 2.
