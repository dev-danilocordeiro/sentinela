# ADR 0024: Chaves de modelo revistas

- Status: aceita
- Data: 2026-10-10

## Contexto

Uma revisão do fluxo depois da ADR 0023 achou cinco problemas nas chaves de modelo:

- `executor_escalation` e `gate_review` eram obrigatórias no `sentinela.yaml`, mas nenhum código as
  lia. Quem trocasse uma delas não mudava nada.
- A sessão da skill `sentinela-fix`, que coordena os subagentes, subia com o modelo do
  `spec_writer`. Trocar o `spec_writer` para Opus encarecia também a orquestração inteira.
- O intérprete de notas de review usava a chave `analysis` e não tinha como ser trocado sozinho.
- O fallback de recusa era só global. Um time que escolheu Opus caía no mesmo fallback dos outros.
- A estimativa usava o modelo do repo no Analyst e no Judge, então uma troca por time só aparecia
  no custo real.

## Decisão

- **Chaves removidas:** `executor_escalation` e `gate_review`. O Policy Gate continua código
  determinístico, sem modelo (princípio 1). Uma config que ainda as tenha deixa de carregar, com
  erro de campo desconhecido.
- **`orchestrator`:** chave própria, padrão Sonnet no exemplo, com override por repo e por time.
  Sem a chave, vale o modelo do `spec_writer`, que era o comportamento anterior.
- **`notes`:** chave própria do intérprete de notas. Sem a chave, vale o de `analysis`. O time dono
  é o do achado que originou o PR.
- **`refusal_fallback`:** aceito em `repos[].models` e `teams.<time>.models`, com a mesma ordem
  (time, repo, global). No global, o valor continua em `models.refusal.fallback_model`, e a chave
  `refusal_fallback` é recusada lá para não haver dois lugares. Cada item leva o seu fallback
  resolvido, inclusive nos votos extras do Judge.
- **Estimativa:** cada pacote é custeado com os modelos do time dono na triagem, na análise e no
  julgamento, e o pior caso por pacote também. O Architect e os achados dele usam o modelo do repo.
  A coluna de modelo lista todos quando eles se misturam.
- `sentinela config models` mostra também `orchestrator`, `notes` e `refusal_fallback`.

## Consequências

- A estimativa por pacote fica um pouco diferente da anterior mesmo sem overrides, porque soma
  custo pacote a pacote em vez de multiplicar uma média. Isso também respeita as faixas de preço
  por tamanho de prompt.
- As configs locais precisam perder as duas chaves removidas. `orchestrator` e `notes` são
  opcionais nelas.
