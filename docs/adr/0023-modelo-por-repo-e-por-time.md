# ADR 0023: Modelo por repo e por time

- Status: aceita; o intérprete de notas, o fallback de recusa e a estimativa por time foram revistos na ADR 0024
- Data: 2026-10-10

## Contexto

Os modelos eram globais: um por papel no bloco `models:`. Quem adota o Sentinela quer trocar
o modelo só em parte da instalação. Exemplos: Sonnet em vez de Haiku na triagem de um repo
difícil, ou Opus em vez de Sonnet na análise e no julgamento de um domínio sensível.

## Decisão

- `repos[].models` e `teams.<time>.models` aceitam as mesmas chaves de papel do bloco global.
  Só as chaves preenchidas sobrescrevem.
- **Ordem:** o time dono vence o repo, e o repo vence o global. O time pesa mais porque o risco
  segue o domínio (o mesmo time de pagamentos pode estar em vários repos).
- **Time dono por papel:**
  - Triager e Analyst: o dono do pacote.
  - Judge e linha de execução (spec writer, builder, reviewers, approver, shepherd): o dono do
    achado gravado no `routing`.
  - Architect, Curator e intérprete de notas: só o nível do repo, porque olham o repo inteiro.
- **Shepherd:** um override de `reviewer_code` sem `shepherd` leva o shepherd junto. É o mesmo
  padrão do bloco global.
- **Validação:** modelos de override precisam de preço. `refusal` e `use_batch` continuam só no
  global, porque a política de recusa e o batch são da rodada, não do papel.
- **Diagnóstico:** `sentinela config models --repo R --team T` mostra o modelo final de cada papel
  e de onde ele veio, como o `owners explain` faz para o dono.
- Os padrões do projeto não mudam.

## Consequências

- Uma rodada em batch pode misturar modelos. A Batch API aceita um modelo por pedido, e o
  `llm_calls` já grava o modelo de cada chamada.
- A estimativa usa o modelo de cada pedido na triagem. No Analyst e no Judge, que ainda não sabem
  o dono final, usa o modelo do repo, então um override por time aparece só no custo real.
