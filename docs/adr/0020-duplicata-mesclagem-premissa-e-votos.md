# ADR 0020: Duplicata humana, mesclagem determinística, premissa como pergunta e votos do Judge

> Nomes de repositórios, times e issues neutralizados em 2026-10-10 (ex.: `app-java-angular`, `EX-9`). Medições, números, datas e conclusões são de execuções reais e não mudaram.

- Status: aceita
- Data: 2026-10-10

## Contexto
A comparação depois da correção do contexto (ADR 0019) mostrou quatro problemas:
- Duplicate no Linear era tratado como recusa, e virava supressão e exemplo de recusa.
- EX-6 e EX-8 se mesclavam uma na outra no rejulgamento.
- Achados que dependem de uma decisão de produto saíam como issue normal.
- Vereditos perto do limiar mudavam de lado entre rodadas (autorização no DELETE do app-java-angular,
  nome social no monorepo-poliglota).

## Decisão

### 1. Duplicate é merge humano
- Issue movida para Duplicate no Linear: o sync lê a original pela relação `duplicate`. A relação
  foi conferida por introspecção.
- O sync grava o outcome `duplicate_of` (`decided_by human`, ator do histórico; ver ADR 0022) e liga o achado
  ao original (`findings.merged_into`).
- O achado assume o estado do original e o acompanha: `PropagateMergedState` roda em cada mudança
  de estado, no sync e no worker.
- Sem supressão, sem exemplo de recusa e fora do Curator. A duplicata sai do acompanhamento, dos
  exemplos do Judge e da checagem de sobreposição.
- Original fora do Sentinela, ou ausente: o achado vira `stale`, com o motivo.

### 2. Mesclagem determinística
`MergeDirection`, entre dois achados equivalentes:
- publicado absorve não publicado;
- dois publicados: a issue mais antiga absorve a mais nova;
- nenhum publicado: o achado mais antigo absorve o mais novo;
- um candidato ainda não gravado é sempre o mais novo.

A direção fica em `run_items` (`merge_direction`) e no `rejudge`. No par real, a EX-6 absorve a
EX-8. Nenhuma issue publicada é alterada: a direção é recomendação para o dono.

### 3. Premissa de produto vira pergunta
- **Limiar:** veredito com `premise` precisa de limiar + `discovery.premise_margin` (0,10). Substituído pela ADR 0054: a pergunta não passa por limiar.
- **Publicação:** acima dele, a issue sai com a label `labels.question` (`sentinela-pergunta`),
  título em forma de pergunta (a primeira frase da premissa), o achado e a evidência no corpo, e
  as instruções de resposta. A premissa fica em `findings.premise`.
- **Resposta, sempre por uma pessoa:**
  - Label `labels.not_intentional`: a mesma issue vira o achado normal (título, corpo com
    critérios, sem a label de pergunta).
  - Label `labels.intentional` (ou cancelar com comentário): recusa com supressão e o motivo, que
    é o último comentário humano. O Sentinela não fecha a issue.
  - A resposta fica em `findings.premise_answer`.
- **Comentários do Sentinela:** saem com a chave pessoal do Linear e sempre começam com
  `**Sentinela`; não contam como motivo de decisão humana.

### 4. Votação perto do limiar
- Veredito de publicação a menos de `discovery.revote_band` (0,10) do limiar efetivo roda o Judge
  `discovery.judge_votes` vezes (3, em chamadas independentes).
- Vale o voto mediano pela confiança em publicar (votos que não publicam contam zero). Com número
  par, vale o de baixo.
- Os votos ficam em `run_items` (`votes`).

### `sentinela rejudge`
- Julga de novo os achados já decididos com o Judge e as regras atuais.
- Não grava achado nem publica.
- Fica numa rodada `mode = rejudge`, com agente `rejudge`, que a memória de decisões da descoberta
  não lê.

## Esquema
Migration `00012`, com `down`:
- `outcomes.kind` ganha `duplicate_of`;
- `findings` ganha `merged_into`, `premise` e `premise_answer`;
- `analysis_runs.mode` ganha `rejudge`.

## Consequências
- As três labels novas precisam existir no Linear: o `config validate` acusa as que faltam.
- A votação custa até duas chamadas a mais por achado na faixa do limiar.
