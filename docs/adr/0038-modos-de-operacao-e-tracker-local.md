# ADR 0038: Modos de operação, com o tracker local como primeira ponta

- Status: aceita (etapa 1: modo local e base comum; o modo MCP é a etapa 2)
- Data: 2026-10-11

## Contexto

Para testar o Sentinela em outro lugar, hoje é preciso criar um app OAuth no Linear, um bot no
Slack e duas GitHub Apps antes de ver o primeiro achado. O dono pediu modos de operação, em que
só as pontas mudam:

- a saída dos achados;
- a entrada das decisões;
- os avisos;
- o host git.

O Postgres continua obrigatório em todos os modos. Ele é o harness de achados, supressões,
decisões, fila e métricas.

## Decisão

### Adaptadores no `sentinela.yaml`

Os valores padrão reproduzem o modo completo de antes:

```yaml
adapters:
  tracker: linear_api | local
  notifier: slack_api | none
  git_host: github_apps | gh_cli
  local_dir: reports/local
```

- **`tracker: local`:**
  - Ao fim de cada rodada, o scan regenera em `local_dir` um relatório por repo, em markdown e HTML
    (`owner__repo.md` e `.html`), mais o `index.md`. Isso substitui o `linear.Publisher`, pela
    interface `discovery.Publisher`. Nada é criado fora da máquina, e o limite por time não se
    aplica, porque não há board para inundar.
  - O relatório separa os achados em abertos, em correção e decididos. Cada achado traz o id
    (`F-<id>`) e o comando de cada ação possível no estado atual, com o `--config` usado.
  - O `serve` não sincroniza nada e só roda a curadoria. O `Enqueue` do worker não lê tracker.
- **`notifier: none`:** o roteador de avisos fica nulo, mesmo com `SLACK_BOT_TOKEN` no ambiente.
- **`git_host: gh_cli`:**
  - O token sai de `gh auth token` e serve para clone, push, PR em draft e a review do gate.
  - A review fica sempre como `COMMENT`, porque é a conta da própria pessoa e o GitHub não deixa
    aprovar o próprio PR. O worker trava o `APPROVE` com gh_cli, e o `config validate` recusa
    `reviewer_approves: true` junto com gh_cli.

### Decisões sem tracker: `sentinela finding`

- **`list` e `show <id>`** funcionam em qualquer modo.
- **`accept`, `reject` e `fix`** só funcionam com `tracker: local`. No Linear, a decisão continua
  na issue: duas portas para a mesma decisão abririam espaço para divergência.
- **`reject <id> "<motivo>"`** grava o mesmo que cancelar a issue:
  - o achado vira `rejected`, com o motivo;
  - entra um outcome `rejected` com `decided_by human`;
  - entra a supressão pelo fingerprint.

  Tudo numa transação, como o `Syncer.resolve`.
- **`accept <id>`** grava o outcome `accepted` com `decided_by human`. O achado continua aberto,
  esperando a correção, e conta como exemplo aceito no Curator.
- **`fix <id>`** equivale a aplicar a label de gatilho:
  - Enfileira a execução com `issue_identifier` `F-<id>` (a branch é `sentinela/F-<id>`) e
    `linear_issue_id` vazio.
  - Registra também o `accepted`, se ainda não houver, porque pedir a correção é aceitar o
    problema.
  - Depois de uma falha sem PR, é uma nova tentativa (`previous_id`). Com job ativo ou PR aberto,
    recusa e aponta o `job rework`.
- **Quem executou:** `--by` (padrão `$USER`) diz quem decidiu. `--executed-by agent --note "..."`
  registra uma decisão humana executada por um agente a pedido de quem decidiu (princípio 6). A
  nota é obrigatória nesse caso. Nunca existe `decided_by agent` pela CLI.

### Worker sem Linear

- Sem cliente do Linear, ou sem issue:
  - os comentários viram no-op, pelo helper `Worker.comment`;
  - labels e mudanças de estado também viram no-op;
  - a spec é montada do próprio achado (`linear.LocalDescription`), sem instruções da equipe.
- O PR não traz `Fixes`: traz "Achado F-<id> do Sentinela (`sentinela finding show <id>`)". O
  relatório local lê o estado da execução e do PR no Postgres.

### Labels por organização e por time

- `labels.finding` vale `sentinela` quando não é definida.
- `teams.<time>.labels.{trigger,done,failed}` troca as labels de gatilho, de resultado e de
  falha só daquele time. Vale `Config.LabelsFor(time)`.
- O `Enqueue` confere a label de gatilho do time dono de cada issue. Para isso, as queries
  `TriggerCandidates` e `RetriggerCandidates` passam a devolver `linear_issues.team`; não há
  mudança de esquema.
- O resultado e a retirada do gatilho usam as labels do time. O worker nunca aplica nenhuma label
  de gatilho, seja da organização ou de um time.
- Com tracker, o `config validate` confere se as labels trocadas existem no time do Linear
  (erro). As da organização continuam como aviso.

### `sentinela init --mode local`

Os passos, em ordem:

1. Sobe o Postgres do docker compose.
2. Cria o `.env` com o `DATABASE_URL` local, só se ele não existir e a variável não vier do
   ambiente.
3. Espera a conexão e aplica as migrations.
4. Gera o `sentinela.yaml` mínimo (`tracker: local`, `notifier: none`, `git_host: gh_cli`, os
   modelos e preços padrão, um time `dono` e os `--repo` dados). Nunca sobrescreve um que exista.
5. Confere o gh logado, o claude, o git e os scanners, que são opcionais.

Ao final, lista o que falta e sai com erro se houver pendência obrigatória. As pontas externas são
injetáveis (`initHooks`) para os testes.

### O que o teste de ponta a ponta mostrou

O teste rodou em 2026-10-11 no banco `sentinela_eval`, no acme/go-svc.

1. **O `gh auth token` devolvia o `GITHUB_TOKEN` do `.env`, e não a conta logada.** O gh dá
   preferência a essa variável, que no Sentinela é o token de leitura dos clones. O push falhou
   com 403. Agora o gh roda sem `GITHUB_TOKEN` no ambiente. `GH_TOKEN`, que é do próprio gh,
   continua valendo.
2. **O relatório não mostrava o fim do job.** Agora o worker o regenera quando um job roda ou um PR
   muda.
3. **Um PR esperando gente não tinha próximo passo no relatório.** Agora o relatório mostra
   `job approve` (no ESCALATE) e `job rework`.
4. **A spec do modo local dizia "cancele a issue".** Agora o rodapé aponta para
   `sentinela finding`.

## Consequências

- **Migração entre modos:** sem esquema novo. Os achados e decisões do modo local ficam no mesmo
  Postgres. Ao trocar para `linear_api`, os achados abertos sem issue (`PendingFindings`) são
  publicados na próxima rodada, e as decisões já tomadas continuam valendo, incluindo supressões,
  few-shot e métricas.
- **O que o modo local não tem:** comentários da equipe na spec, avisos e auto-aprovação.
- **O relatório local é uma visão:** ele é regenerado do Postgres a cada rodada e a cada decisão.
  Editar o arquivo não muda nada.
