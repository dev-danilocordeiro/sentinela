# ADR 0027: Issue em revisão quando o worker abre o PR

- Status: aceita
- Data: 2026-10-10

## Contexto

Depois de abrir o PR, o worker comentava na issue e aplicava `sentinela-done`, mas a issue ficava
no estado em que estava (Backlog, na EX-14). O board não mostrava que havia um PR esperando
revisão.

## Decisão

- Quando o worker abre ou atualiza o PR de um job, ele move a issue para `linear.review_state` do
  time dono (`teams.<time>.linear.review_state`, senão `defaults.linear.review_state`). Exemplo:
  `In Review`.
- **É registro de fato, não decisão humana:** existe um PR. O princípio 6 continua valendo:
  concluir, cancelar, marcar duplicata, aplicar `sentinela-fix`, aprovar e mergear são das pessoas.
- **Proteções:**
  - issue concluída, cancelada ou duplicada não é mexida;
  - issue que já está no estado de revisão não é mexida;
  - sem `review_state`, nada se move (padrão).
- **Estado de revisão:** precisa existir no time. O `config validate` confere. Num time sem o
  estado, o worker só avisa no log.
- **Efeito no sync:** o estado de revisão é do tipo `started`, e o sync só reage a concluída,
  cancelada e duplicada. Mover para revisão não gera desfecho.
- A mutation é `issueUpdate` com `stateId`, conferida por introspecção em 2026-10-10.
