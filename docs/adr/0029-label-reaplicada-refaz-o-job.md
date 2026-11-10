# ADR 0029: Aplicar a label de novo refaz o job que terminou sem PR

- Status: aceita
- Data: 2026-10-10

## Contexto

O gatilho só enfileirava achados sem nenhuma execução. Depois de um job `failed` ou `blocked`
(sem PR), a única forma de tentar de novo era `sentinela job retry` no terminal. Na EX-15, o
builder bloqueou porque a correção exigia mexer em `go/internal/turmas`, fora do escopo. A pessoa
comentou no card liberando o pacote, mas não havia como pedir a nova tentativa pelo card. Em
produção, o time trabalha no Linear e não deveria precisar de terminal para isso.

A primeira versão usava o gesto "tirar e recolocar a label", lido do histórico da issue. Não
funcionou: quando a mesma pessoa tira e recoloca a label em segundos, o Linear grava uma entrada
só, sem label adicionada nem removida (`addedLabelIds` nulo, `removedLabelIds` vazio,
`changes` nulo; visto na EX-15 em 2026-10-11T00:44:16Z).

## Decisão

- Quando um job termina `failed` ou `blocked` sem PR, o worker **tira a label de gatilho** da
  issue e registra isso na execução (`executions.artifacts["trigger-consumed"]`). O comentário do
  fim do job explica como pedir de novo.
- **Aplicar a label de novo** é o pedido de nova tentativa. O worker enfileira um job igual ao do
  `job retry` (tipo `fix`, `previous_id` da anterior, a partir da branch padrão) quando:
  - o achado está aberto e a última execução não cancelada terminou `failed` ou `blocked`, sem
    PR e sem execução ativa;
  - essa execução tem o registro `trigger-consumed`;
  - a issue tem a label de gatilho e não está concluída, cancelada nem duplicada.
- O registro só é gravado depois de a label sair. Assim a label antiga nunca é tomada por um
  pedido novo. Se a remoção falhar no fim do job, o próximo ciclo do `Enqueue` tenta de novo. O
  mesmo ciclo recupera as execuções que terminaram antes desta regra: tira a label e comenta.
- No card, o fluxo é: comentar o que muda e aplicar a label de novo. Os comentários desde a
  execução anterior entram na spec como Instruções da equipe (ADR 0026). O worker comenta na
  issue qual execução a nova tentativa refaz.
- **Princípio 6:** a decisão de tentar de novo é de quem aplica a label. Tirar a label não decide
  nada, só registra que o pedido foi atendido, como o `review_state` (ADR 0027). O worker nunca
  aplica a label de gatilho.
- **Fora desta regra:**
  - job com PR (ESCALATE ou APPROVE): a label fica e o caminho é o PR. Pedir mudança na review
    aciona o PR Shepherd, e `job rework` continua no terminal;
  - comentário sozinho não refaz o job. Cada conversa no card reiniciaria a execução, e um
    comando no texto exigiria validar quem escreveu.
- `job retry` e `job rework` continuam valendo.
- Não há mudança no esquema: o registro usa a coluna `artifacts` (JSONB) que já existe.
