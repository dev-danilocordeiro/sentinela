# ADR 0004: Contabilidade de custo e orçamento por rodada

- Status: aceita
- Data: 2026-10-09

## Decisão
- Cada chamada (sucesso, erro de API, recusa, item de batch com erro/expirado) grava uma linha em
  `llm_calls` com agente, modelo, versão do prompt, modo, tokens por tipo, custo, latência e IDs
  (`message_id`, `batch_id`, `custom_id`). Com `run_id`, o custo é somado em `analysis_runs.cost_usd`
  na mesma transação.
- `llm.Budget` é por rodada. Antes de cada chamada, `Check` recusa se o gasto já atingiu o limite.
  Depois, `Charge` sempre contabiliza o custo incorrido e devolve `ErrBudgetExceeded` se passou do limite;
  o chamador deve abortar a rodada (`analysis_runs.status = aborted_budget`).
- Batch: antes de enviar, estima o pior caso (entrada por bytes/3 + `max_tokens` de saída, já com
  desconto de batch) e recusa o batch inteiro se não couber no saldo. Assim um batch nunca
  estoura o orçamento por mais do que a estimativa erra.
- Latência de item de batch é o tempo total do batch (envio até `ended`).

## Consequências
Uma chamada síncrona pode passar do limite em no máximo o custo dela mesma. Orçamento por PR e
por mês usam o mesmo `Budget` nas Fases 4 e 3, respectivamente.
