# ADR 0015: Publicação no Linear, feedback e avisos no Slack (Fase 3)

> Nomes de repositórios, times e issues neutralizados em 2026-10-10 (ex.: `app-java-angular`, `EX-9`). Medições, números, datas e conclusões são de execuções reais e não mudaram.

- Status: aceita
- Data: 2026-10-10

## Publicação
- O publisher (`internal/linear`) é código determinístico. Ele publica os achados `open` sem issue
  principal, por severidade e confiança do juiz, com no máximo
  `publish.max_issues_per_team_per_run` issues por time por rodada (principais e relacionadas).
  O excedente fica pendente para a próxima rodada; uma rodada `no_changes` também publica pendentes.
- Board, projeto, estado inicial, responsável (`none`, `lead`, `round_robin` por e-mail) e labels
  vêm do `routing` gravado no achado e do `sentinela.yaml`. A prioridade vem da severidade
  (critical 1 … low 4).
- Labels `claude-*` pertencem a outro sistema. O `Validate` da config as rejeita e o publisher as
  filtra de novo.
- Idempotência: o corpo da issue termina com `sentinela-fingerprint: <fp>`. Antes de criar, o
  publisher procura esse marcador no time (`description.contains`). Uma queda entre criar no Linear
  e gravar no Postgres não duplica a issue. (Desde a ADR 0047 o marcador leva o id da instalação,
  e a busca ignora issue arquivada, na lixeira ou fora do time e do projeto do roteamento.)
- Achado entre módulos: a issue principal vai para o time do arquivo da evidência principal. Cada
  outro time afetado recebe uma issue relacionada, no próprio board, ligada à principal
  (`issueRelationCreate`, tipo `related`). Módulos do mesmo time não geram issue extra.
- Tabelas novas (migration 00007, aprovada): `linear_issues`, `feedback_examples` e `thresholds`.
  O contrato `Finding` não mudou.

## Sync de estado
- `sentinela serve` funciona por polling (padrão) ou por webhook. Os dois passam por `Syncer.Apply`
  e são deduplicados em `webhook_events`:
  - polling: `event_id` = `poll:<issue>:<updatedAt>`;
  - webhook: `event_id` = `Linear-Delivery`, com `Linear-Signature` (HMAC-SHA256 do corpo cru)
    validado e `webhookTimestamp` dentro de 1 minuto.
- O webhook relê a issue na API em vez de confiar no payload.
- Issue `canceled` ou `duplicate` vira achado `rejected`, com `outcomes(rejected)` e uma supressão
  pelo fingerprint, numa transação. O motivo é o último comentário humano; o autor vem do
  histórico. Issue `completed` vira `fixed`, com `outcomes(accepted)`.

## Feedback Curator
- Usa o Haiku e roda uma vez por `feedback.curator_interval`; a última execução é a última
  `llm_call` do agente `curator`. Só roda nos repos com desfechos novos.
- Gera exemplos few-shot (com uma lição geral), supressões por glob de caminho (expiram em
  `feedback.suppression_ttl`) e `min_confidence` por módulo e categoria.
- O código recusa glob que cubra o repo inteiro e exemplo de achado fora da entrada, e limita os
  limiares a [0,5; 0,95].
- O Judge recebe primeiro os exemplos curados, e o limiar do módulo mais específico vale no lugar
  de `discovery.min_confidence`.

## Memória dos descartes
- Achados descartados não vão para `findings`. Nas rodadas 7 e 8 do app-java-angular, um problema
  descartado voltou com outras palavras e foi publicado (EX-9), porque o Judge não é
  determinístico.
- Agora as decisões `discard` e `merge` anteriores, lidas de `run_items`, impedem novo
  julgamento do mesmo fingerprint e entram em `similar` como `discarded` e `merged` (prompt do
  Judge v3).
- Na rodada 9 (`--full`), nenhuma issue nova foi criada.

## Limite de uso da assinatura
- A mensagem de limite do CLI vira `ErrUsageLimit`, que embrulha `ErrBudgetExceeded`. A rodada
  para de despachar e termina como `aborted_budget`. Antes, cada item era pulado em silêncio
  (rodada 6).

## Slack
- O mesmo bot posta em vários canais:
  - o resumo da rodada vai para `defaults.notify.slack`;
  - cada issue criada (e, na Fase 4, cada PR) vai para `teams.<time>.notify.slack` do time dono;
  - time sem canal cai no canal padrão.
- O canal aceita ID ou `#nome`; o nome é resolvido uma vez por `conversations.list` e fica em cache.
- `notify.mention` aceita IDs `S…` ou `U…`, `@here` e `@channel`. Um handle sai como texto, porque o
  Slack não notifica grupos por handle.
- Falha de postagem vira aviso, e o canal padrão recebe a mensagem dizendo qual canal falhou e
  por quê.
- As chamadas são form-encoded: `conversations.info` recusa JSON com `invalid_arguments`.
- `config validate` confere cada canal: se existe, se não está arquivado e se o bot é membro.
