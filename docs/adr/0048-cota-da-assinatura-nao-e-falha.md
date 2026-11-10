# 0048: Cota da assinatura não é falha do job

## Contexto

Em 2026-10-14, às 12:27 (São Paulo), a cota semanal da assinatura acabou. O worker pegou sete jobs
em sequência (execuções 24 a 30, EX-17 a EX-28). Cada sessão terminou em segundos com "You've
hit your weekly limit · resets Oct 16, 7am (America/Sao_Paulo)". Para cada uma, o worker:
- gravou `failed`;
- comentou a falha na issue;
- pôs `sentinela-failed`;
- tirou a `sentinela-fix` (ADR 0029).

Para tentar de novo, alguém tinha de reaplicar a label em sete issues, embora nenhum job tivesse
falhado. A ADR 0030 já tratava o limite de uso como falha retomável no retry, mas o retry dependia
dessa decisão humana.

## Decisão

- **Limite de uso** é a mensagem do CLI que `llm.UsageLimited` reconhece: os limites de sessão e
  semanal da assinatura e, agora, também o rate limit da API (`rate limit`, `rate_limit_error`).
- **O job volta para a fila:**
  - `status queued`, sem `failed`, `decision` ou `error`;
  - `wait_reason "cota da assinatura até <dd/mm hh:mm> (<fuso>)"` e `run_after` no reset mais 5
    minutos de margem;
  - sem comentário e sem mexer nas labels da issue;
  - `artifacts.quota` guarda quando parou e a mensagem, e `artifacts.worktree` guarda o estado do
    worktree.
- **Horário do reset:** `llm.UsageResetAt` lê a mensagem (`resets Oct 16, 7am (America/Sao_Paulo)`,
  `resets 5:50pm (...)` ou o formato antigo `limit reached|<epoch>`). Quando a mensagem não diz
  (rate limit), a pausa é de 30 minutos.
- **O worker pausa:** nenhum job começa até o reset, e o PR Shepherd também espera. A pausa fica
  em memória e no banco (`QuotaPausedUntil`: maior `run_after` dos jobs devolvidos pela cota), então
  sobrevive a um restart. O aviso vai uma vez para o canal padrão (`defaults.notify.slack`).
- **Depois do reset:** o mesmo job continua a própria sessão (`--resume`) no mesmo worktree e
  diretório. A nota é "continuou a sessão … depois da pausa pela cota", que não conta para
  `execution.max_resumes`. Se a sessão sumiu ou o worktree mudou, o job recomeça do zero, e os
  arquivos da tentativa interrompida vão para `jobs/N.cota-<instante>`.
- **PR Shepherd:** no limite, não conta ciclo nem comenta; o evento é tratado depois do reset.
- **Scan:** continua como antes. A rodada termina `aborted_budget` sem avançar o `head_sha`, e a
  seguinte analisa o mesmo código, então nenhum achado se perde. O `scan --all` para nos repos
  seguintes. O teste `TestUsageLimitAbortsTheRunWithoutLosingFindings` fixa isso.

## Consequências

- Um job só fica `failed` por limite de uso se o banco recusar o requeue (erro no log).
- As execuções 24 a 30 continuam `failed`: reaplicar a `sentinela-fix` nas issues é decisão humana
  (princípio 6).
