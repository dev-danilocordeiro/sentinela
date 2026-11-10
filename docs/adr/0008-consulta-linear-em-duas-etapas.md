# ADR 0008: Consulta do workspace do Linear em duas etapas

> Nomes de repositórios, times e issues neutralizados em 2026-10-10 (ex.: `app-java-angular`, `EX-9`). Medições, números, datas e conclusões são de execuções reais e não mudaram.

- Status: aceita (substitui a consulta única da ADR 0007)
- Data: 2026-10-09

## Contexto
A ADR 0007 deixou para confirmar as conexões `states`, `projects` e `labels` de `Team`. Na primeira
execução com a chave real (workspace de teste), a consulta única
`teams(first: 100) { states(first: 100) projects(first: 250) labels(first: 250) }` foi recusada com
HTTP 400 `Query too complex` (complexidade 66.130; o limite é 10.000), antes de validar os campos.

Testado direto na API: `team(id) { states(first: 100) { nodes { name } pageInfo { ... } } }`,
e o mesmo para `projects` e `labels`, funciona. O time de teste (EX) trouxe 7 estados (sem Triage), 14 projetos
e 12 labels, inclusive as 4 do Sentinela.

## Decisão
- Etapa 1: `teams(first: 50, after: $cursor) { nodes { id key name } pageInfo }`, paginado.
- Etapa 2: para cada time e cada conexão, `team(id: $id) { <conexão>(first: 100, after: $cursor) }`,
  paginado por `pageInfo.endCursor`. Cada consulta fica bem abaixo do limite.
- As consultas usam variáveis GraphQL em vez de interpolar valores na string.

## Consequências
São 1 + 3×N requisições (N = times) por validação. Isso é irrelevante para `config validate` e
`bootstrap`. O publisher da Fase 3 deve reutilizar a mesma estratégia e cachear o resultado por rodada.
