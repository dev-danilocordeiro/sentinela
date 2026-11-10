# ADR 0035: Scan agendado incremental, com no máximo N repos novos por rodada

- Status: aceita
- Data: 2026-10-11

## Contexto

O `scan --all` (ADR 0034, operação) rodava todos os repos do `sentinela.yaml`. Seis deles nunca
tinham rodado. A primeira rodada de cada um analisa o repo inteiro, então a primeira segunda-feira
juntaria seis rodadas full e provavelmente bateria no limite de uso da assinatura.

## Decisão

- O `scan --all` roda, na ordem do `sentinela.yaml`:
  - todos os repos já analisados, em modo incremental;
  - no máximo `discovery.max_new_repos_per_run` repos nunca analisados.
- O padrão é 1. Com 0, só rodam os repos já analisados. Valor negativo falha no `config validate`.
- **Já analisado** é ter uma rodada `full` ou `incremental` que terminou (`succeeded` ou
  `no_changes`) com `head_sha`. É a mesma regra da base do diff incremental. Um repo novo cuja
  primeira rodada falhou continua novo e volta na semana seguinte, na mesma posição da fila.
- Os novos que ficaram de fora aparecem numa linha no começo da saída, que vai para o journal.
- A publicação segue `publish.enabled` e o limite por time, como no `scan --repo`.

## Consequências

- Com seis repos novos e o padrão 1, todos entram em seis semanas, um por segunda-feira.
- Rodar um repo novo antes da vez continua possível com `scan --repo X`. A partir daí, ele conta
  como já analisado.
