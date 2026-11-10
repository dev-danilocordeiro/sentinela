# ADR 0045: Duplicata e resposta à pergunta de produto no modo local

- Status: aceita
- Data: 2026-10-11

## Contexto

No modo local (ADR 0038), duas decisões do Linear não tinham equivalente na CLI:

- **Duplicate:** o merge humano de dois achados que são o mesmo problema.
- **A resposta à pergunta de produto:** as labels `intentional` e `not_intentional`.

## Decisão

- **`sentinela finding duplicate F-<id> --of F-<id>`:** tem o mesmo efeito de marcar Duplicate no
  Linear (`MarkFindingDuplicate`).
  - O achado mais novo é absorvido pelo mais antigo, mesmo que a ordem venha trocada.
  - O absorvido passa a acompanhar o estado do original.
  - Grava o outcome `duplicate_of`.
  - Não grava supressão nem exemplo de recusa: duplicata não é recusa do problema (ADR 0020).
  - Os dois achados precisam ser do mesmo repo e estar pendentes.
- **`sentinela finding answer F-<id> intentional | not-intentional --note`:** responde à pergunta
  de um achado com premissa de produto.
  - **`intentional`:** o achado vira `rejected`, com a supressão e o motivo
    "É intencional: <nota>", como a label `intentional`.
  - **`not-intentional`:** a pergunta vira achado normal, aberto e esperando a correção, e grava o
    outcome `accepted`. A pessoa confirmou que o problema é real.
- **Os três modos gravam o mesmo.** No modo completo e no MCP, a label `not_intentional` numa
  pergunta também grava o outcome `accepted` com `decided_by human`:
  - com quem aplicou a label, pelo histórico da issue, quando o tracker sabe dizer;
  - sem o autor no MCP, que não dá o histórico.
- **Comum aos dois:**
  - gravam `decided_by human` (e `executed_by agent` com nota, quando um agente executa a pedido);
  - só funcionam no modo local;
  - o relatório local mostra o comando de cada um: duplicata em todo achado aberto, resposta
    enquanto a pergunta está aberta.
