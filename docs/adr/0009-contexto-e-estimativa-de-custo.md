# ADR 0009: Corte de contexto do Triager e estimativa de custo antes da rodada

- Status: aceita
- Data: 2026-10-09

## Contexto
O Haiku 5.5 custa US$ 0,10/0,50 por MTok até 100K tokens de prompt e US$ 0,50/2,50 acima disso.
Um pacote grande mandado inteiro ao Triager pode multiplicar o custo da triagem por cinco.
Além disso, o operador precisa saber quanto uma rodada vai custar antes de ela começar.

## Decisão
- Tokens são estimados sem chamar a API, a 2 bytes por token (`agents.BytesPerToken`). Código rende
  3 a 4 bytes por token, mas JSON com código escapado e o tokenizer novo (que conta ~30% a mais)
  puxam para baixo. O primeiro valor testado, 2,5, foi derrubado pelo teste: a 2 bytes por token,
  um pacote grande passaria de 100K.
- O Triager tem teto configurável `discovery.triage_context_tokens` (90.000; a validação recusa
  valores ≥ 100.000). `triager.Fit` corta, nesta ordem: vizinhos, sinais de scanner além de 50 e,
  por último, metade do maior arquivo por vez (abaixo de 2 KB, omite). Todo corte vai em `truncated`
  para o modelo saber o que não viu, e vira aviso na rodada.
- Depois da chamada, se `usage` mostrar mais de 100K tokens de prompt, a rodada registra aviso.
- Antes da rodada, `discovery.estimate` calcula por agente um valor esperado e um pior caso:
  - Triager: entrada medida em cada requisição já cortada; saída esperada de 1.000 tokens.
  - Analyst: metade dos pacotes aprofundados, saída de 6.000 tokens.
  - Architect: uma chamada.
  - Judge: dois achados por pacote aprofundado.
  - Pior caso: todos aprofundados, `max_tokens` de saída e o máximo de achados.
- Se o esperado passar do orçamento, a rodada nem começa (`ErrEstimateOverBudget`).
  `--estimate-only` mostra a tabela sem chamar modelos. O pior caso é garantido lote a lote pelo
  `llm.Budget` (ADR 0004), então o gasto real nunca passa do orçamento.

## Consequências
A estimativa é conservadora na entrada e chuta a saída. O relatório de cada rodada mostra o real,
e as premissas devem ser recalibradas com os dados de `llm_calls` depois de algumas rodadas.
