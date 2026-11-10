# ADR 0021: Pergunta de produto obrigatória no Judge e votação desligada por padrão

> Nomes de repositórios, times e issues neutralizados em 2026-10-10 (ex.: `app-java-angular`, `EX-9`). Medições, números, datas e conclusões são de execuções reais e não mudaram.

- Status: aceita
- Data: 2026-10-10

## Contexto
- **Votação (ADR 0020):** com a mesma entrada, os três votos do Judge saíram idênticos nos casos
  reais (autorização no DELETE e nome social). A votação só custava chamadas.
- **Premissa de produto:** o campo `premise` era opcional e instável. A EX-10 saiu sem premissa
  num rejulgamento e com premissa no seguinte. No primeiro teste do schema novo, a resposta mudou
  entre execuções: uma "não" e duas "sim".

## Decisão
- **Votação:** `discovery.judge_votes` passa a ter padrão 1 (desligada). O código fica: com
  `judge_votes > 1`, a votação na faixa do limiar volta a valer.
- **Saída do Judge:**
  - O campo opcional `premise` dá lugar a `product_decision: {depends, decision}`, obrigatório em
    todo veredito, inclusive descarte e mesclagem.
  - É o primeiro campo da saída: o Judge responde antes de decidir.
  - O `Parse` recusa `depends` sem decisão, zera a decisão quando `depends` é falso e deriva
    `Verdict.Premise` dela. O resto do fluxo (limiar com margem, pergunta ao time) continua igual.
- **Prompt v6 do Judge:** traz critérios objetivos, com exemplos reais dos repos.
  - "Sim" quando a correção muda o que uma pessoa de fora pode fazer, ver ou esperar (acesso, dados
    expostos, retenção e durabilidade, limites por cliente) e o código é coerente com o
    comportamento atual.
  - "Não" para defeitos em relação ao que o próprio código pretende.
  - Empate: "sim".

## Medição
- `sentinela rejudge --only EX-10`, cinco vezes, deu "sim" nas cinco, com a mesma pergunta (o texto
  varia um pouco).
- O rejulgamento dos 40 achados marcou "sim" só para acesso, dados expostos e durabilidade em
  memória, e "não" para os defeitos técnicos.
- A confiança da EX-10 oscilou entre 0,70 e 0,65, em cima do limiar elevado (0,70). Por isso a
  pergunta sai numa execução e é descartada em outra. É o caso que a votação estabiliza, se for
  religada.
