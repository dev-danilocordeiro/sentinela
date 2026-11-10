# ADR 0028: Issue concluída quando o worker vê o PR mergeado

- Status: aceita
- Data: 2026-10-10

## Contexto

Quem movia a issue para Done depois do merge era a integração do Linear com o GitHub. Na EX-11,
o PR #5 saiu de draft às 20:44:15 e foi mergeado às 20:44:22. A integração registrou só a
transição do "ready for review" (In Review → In Progress), e a do merge se perdeu. O PR estava
mergeado, mas a issue ficou em andamento. Na EX-14, com 12 minutos entre os dois eventos, a
integração moveu a issue para Done normalmente.

## Decisão

- Quando o acompanhamento de PRs do worker vê o PR de uma execução mergeado, ele grava o
  desfecho `merged` e, em seguida, move a issue para `linear.done_state` do time dono
  (`teams.<time>.linear.done_state`, senão `defaults.linear.done_state`, senão `Done`).
- **É registro de fato, não decisão humana:** o merge foi feito por uma pessoa, e o desfecho
  `merged` fica com `decided_by = human`, como antes. Vale o mesmo raciocínio do `review_state`
  (ADR 0027).
- **Proteções:**
  - issue concluída, cancelada ou duplicada não é mexida (a decisão da pessoa no Linear vence);
  - estado que não existe no time só gera aviso no log;
  - o `config validate` confere o estado de cada time.
- **Ordem:** a issue só se move depois que o desfecho é gravado e o achado fica `fixed`. Assim,
  quando o sync do Linear vê a issue concluída, o achado já não está pendente e não há um segundo
  desfecho (`accepted`).
- O Sentinela não depende mais da integração do Linear com o GitHub para fechar a issue. Se a
  integração também mover, a proteção de "já concluída" evita a segunda transição.
