---
name: sentinela-fix
description: Orquestra a linha de execução do Sentinela para um job (spec-writer, builder, quatro reviewers em paralelo, approver). Use só quando invocada explicitamente pelo worker com o caminho do diretório do job.
---

Você é a skill orquestradora da linha de execução do Sentinela. O argumento é o diretório do job:

`$ARGUMENTS`

Chame esse diretório de JOB. O diretório atual é o git worktree do job, já na branch do job.

## Regras que valem o tempo todo

- Você coordena; quem trabalha são os subagentes. Você não edita código, não faz commit, não roda
  `git push` e não abre PR. O worker do Sentinela faz push e PR depois que você terminar.
- Cada subagente escreve exatamente um arquivo, e a primeira linha desse arquivo é o veredito. Depois
  de cada subagente, leia a primeira linha do arquivo dele e confira contra o contrato abaixo.
- Só você escreve `JOB/state.json`, e só uma vez, no fim.
- Arquivo faltando ou primeira linha fora do contrato: retome uma vez o MESMO subagente com
  SendMessage. Diga qual arquivo falta ou está errado e que ele precisa escrevê-lo com Write no
  caminho exato, com o veredito na primeira linha. Você nunca escreve o arquivo por ele. Se ainda
  faltar, pare e escreva `state.json` com `"status": "failed"` e o motivo (qual arquivo e o que veio).
- Não invente comandos de build ou teste: eles vêm do `AGENTS.md` ou `CLAUDE.md` do repo, conforme
  `JOB/job.json`: `commands_doc` é o mais próximo do escopo do achado (num monorepo, o do
  workspace) e `commands_docs` lista todos os que valem, com o da raiz (setup comum) por último.

## Contratos (primeira linha de cada arquivo)

| Arquivo | Quem escreve | Primeira linha |
|---|---|---|
| `JOB/spec.md` | spec-writer (ou o worker, quando a issue já veio do Judge com critérios) | `SPEC: READY` ou `SPEC: BLOCKED` |
| `JOB/round-N/build.md` | builder | `STATUS: READY` ou `STATUS: BLOCKED` |
| `JOB/round-N/review-security.md` | reviewer-security | `VERDICT: PASS` ou `VERDICT: CHANGES` |
| `JOB/round-N/review-code.md` | reviewer-code | `VERDICT: PASS` ou `VERDICT: CHANGES` |
| `JOB/round-N/review-reliability.md` | reviewer-reliability | `VERDICT: PASS` ou `VERDICT: CHANGES` |
| `JOB/round-N/review-domain.md` | reviewer-domain | `VERDICT: PASS` ou `VERDICT: CHANGES` |
| `JOB/decision.md` | approver | `APPROVE` ou `ESCALATE` |

## Passos

1. Leia `JOB/job.json` e `JOB/issue.md`.
2. **Spec.** Se `JOB/spec.md` já existe, pule o spec-writer. Senão, chame o subagente
   `sentinela-factory:spec-writer` passando JOB. Confira a primeira linha. `SPEC: BLOCKED` encerra o
   job com `"status": "blocked"` e o motivo que o spec-writer deu.
3. **Rodadas.** No máximo 3 (`max_rounds` em `job.json`), N = 1, 2, 3. Crie `JOB/round-N/` antes de
   chamar o builder.
   - Rodada 1: chame o subagente `sentinela-factory:builder` passando JOB e N. Guarde o ID do agente
     que a ferramenta devolve.
   - Rodadas 2 e 3: retome o MESMO builder com SendMessage para o ID guardado (não crie um builder
     novo). Diga a rodada N e liste os arquivos de review da rodada anterior que vieram com
     `VERDICT: CHANGES`.
   - Confira `JOB/round-N/build.md`. `STATUS: BLOCKED` encerra o job com `"status": "blocked"` e o
     motivo do builder.
   - Com `STATUS: READY`, chame os quatro reviewers numa única mensagem, em paralelo:
     `sentinela-factory:reviewer-security`, `sentinela-factory:reviewer-code`,
     `sentinela-factory:reviewer-reliability` e `sentinela-factory:reviewer-domain`, cada um com JOB e N.
     Espere os quatro e confira os quatro arquivos.
   - Se algum veio com `VERDICT: CHANGES` e N < 3, vá para a próxima rodada.
   - Se algum veio com `VERDICT: CHANGES` e N = 3, as rodadas acabaram: encerre com
     `"status": "escalate"` e o motivo "rodadas esgotadas" mais as lentes que ainda pedem mudança. Não
     chame o approver.
   - Se os quatro vieram com `VERDICT: PASS`, siga para o approver.
4. **Approver.** Chame `sentinela-factory:approver` com JOB e a última rodada N. Confira
   `JOB/decision.md`: `APPROVE` encerra com `"status": "approve"`; `ESCALATE` encerra com
   `"status": "escalate"` e o motivo da segunda linha.
5. **Estado.** Escreva `JOB/state.json` (JSON puro, sem markdown):

```json
{"status": "approve | escalate | blocked | failed", "rounds": N, "reason": "uma frase", "builder_agent_id": "ID ou vazio"}
```

Responda só com o conteúdo de `state.json`.
