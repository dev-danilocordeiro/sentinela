# 0050: Limite por time na rodada inteira do scan --all

## Contexto

M8 da revisão de 2026-10-11. `publish.max_issues_per_team_per_run` contava por time **e por
repo**. Cada repo do `scan --all` publicava logo depois da sua análise, com um contador próprio.
Com os três `ledger-*` novos, o financeiro poderia receber até 12 issues de uma vez (3 por repo). E
a ordem por severidade valia só dentro de cada repo: os achados low do primeiro repo tomavam as
vagas antes de um critical do último.

## Decisão

- **Quando publica:** no `scan --all`, cada repo só registra que terminou a análise (`linear.Round`).
  Isso vale para os repos com `succeeded` ou `no_changes`, os mesmos que publicavam antes. A
  publicação sai uma vez, no fim, também quando o limite de uso da assinatura para a rodada no
  meio.
- **Contagem e ordem:** o limite conta por time na rodada inteira, somando os repos, como antes:
  principais e relacionadas. A ordem é a do `PendingFindings`, agora entre os repos: severidade,
  confiança do Judge, id. O excedente fica pendente para a rodada seguinte.
- **Resumos:** o resumo de cada repo diz que a publicação ficou para o fim. Um resumo da
  publicação vai ao canal padrão (`defaults.notify.slack`) com as issues novas e os adiados por
  time e severidade, por exemplo `financeiro 4 (high 1, low 3)`. O aviso de cada issue continua
  indo ao canal do time.
- **scan --repo:** continua igual, com o limite por time daquela rodada.
- **Interrupção:** um `scan --all` cancelado (SIGTERM) não publica, e os pendentes ficam para a
  próxima rodada.

## Consequências

- Teste `TestRoundSharesThePerTeamLimitAcrossReposBySeverity`: são três achados low e um medium em
  `acme/mono` e um critical, um high e um low em `acme/svc`. Saem critical, high e medium. Os três
  low ficam adiados e saem na rodada seguinte.
- `TestRoundSummaryShowsDeferredByTeamAndSeverity` confere o resumo.
- As issues continuam gravadas com a rodada do repo de cada achado (`linear_issues.run_id`), então
  o `report --run` de cada repo não muda.
- O aviso de cada issue no canal do time sai no fim do `scan --all`, não mais no fim de cada repo.
