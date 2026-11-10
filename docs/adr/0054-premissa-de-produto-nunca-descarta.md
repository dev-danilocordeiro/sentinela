# 0054: Achado que depende de produto sai como pergunta, nunca como descarte

## Contexto

No teste num monorepo Java e TypeScript, o Judge descartou com 62% o achado "o canal WebSocket do
KDS aceita o tenant pela query, sem identidade autenticada": qualquer cliente lê os pedidos de
outro restaurante. O motivo foi "a correção muda quem pode acessar o canal, então depende de
decisão de produto". É exatamente o caso que a ADR 0020 queria transformar em pergunta ao time.

Dois caminhos levavam ao descarte:

- **Limiar elevado:** veredito com premissa precisava de `min_confidence + premise_margin` (0,70).
  O prompt mandava reduzir a confiança quando o achado dependia de produto, então a pergunta
  ficava quase sempre perto ou abaixo do limiar. A ADR 0021 já registrava o efeito: "a pergunta
  sai numa execução e é descartada em outra".
- **Discard do Judge:** com `product_decision.depends: true` e `decision: discard`, o achado caía
  no descarte comum, sem passar pela pergunta.

## Decisão

- **Regra no código (`discovery.decide`):** veredito com premissa de produto vira publicação como
  pergunta (`labels.question`), qualquer que seja a confiança e mesmo com `discard`. Só o `merge`
  com um achado aberto vence a pergunta. Vale para a descoberta, as notas de review e o
  `sentinela rejudge`, que usam a mesma função.
- **Votação:** só para vereditos sem premissa. A pergunta não passa por limiar, então não há o
  que estabilizar.
- **`discovery.premise_margin`:** fica sem efeito. O campo continua aceito para não quebrar configs
  antigas; a documentação diz que não vale mais.
- **Prompt v8 do Judge:**
  - com `depends: true`, a decisão é `publish` (ou `merge`), nunca `discard`;
  - a confiança não cai por depender de produto: ela mede se o comportamento descrito existe no
    código;
  - se a evidência não mostra o comportamento, o achado não depende de nada: `depends: false` e
    descarte pelo motivo técnico.

## Consequências

- O time vê toda pergunta de produto que o Judge reconhece, e decide com
  `sentinela-intencional` ou `sentinela-nao-intencional` (ADR 0020). O custo é atenção: perguntas
  que antes sumiam abaixo do limiar agora chegam. O filtro contra o ruído é o critério do
  `depends` (ADR 0021), não a confiança.
- **Teste:** `TestProductPremiseIsAlwaysAQuestion` (publish abaixo do limiar e discard, os dois
  com premissa, viram pergunta; sem premissa, o limiar vale). Os testes de votação e de
  rejulgamento passaram para a regra nova.
