# Operação

## Ciclo recomendado

O ciclo abaixo é o do modo completo. No modo local, as decisões entram por `sentinela finding` e
o relatório fica em `reports/local`; no modo MCP, o Linear é lido pelo MCP do Claude Code
([modos.md](modos.md)).

1. **Modo sombra** (`publish.enabled: false`): rode `scan` nos repos, revise os relatórios e ajuste
   a configuração (times, módulos ignorados, limiar).
2. **Publicação**: ligue `publish.enabled`. Os achados aprovados viram issues, no máximo
   `max_issues_per_team_per_run` por time por rodada. No `scan --all`, a rodada é a execução
   inteira: a publicação sai no fim, por severidade entre os repos, e o resumo no canal padrão
   mostra os adiados por time (ADR 0050).
3. **Feedback**: deixe o `serve` rodando para trazer as decisões do time de volta.
4. **Execução**: deixe o `worker` rodando. Ele só age quando alguém aplica `sentinela-fix`.

## Comandos

| Comando | O que faz |
|---|---|
| `sentinela scan --repo R` | Rodada de descoberta (incremental desde o último SHA; `--full` analisa tudo) |
| `sentinela scan --all` | Os repos já analisados e até `discovery.max_new_repos_per_run` novos, um por vez (ADR 0035); relê a config antes de cada repo; um erro não para os outros, o limite da assinatura para tudo |
| `sentinela init --mode local` · `--mode mcp` | Prepara uma instalação: Postgres, migrations, `sentinela.yaml` mínimo e o que falta (ADRs 0038 e 0039) |
| `sentinela finding list` · `show N` | Achados (abertos; `--all` inclui os decididos) e o detalhe de um, em qualquer modo |
| `sentinela finding accept N` · `reject N "motivo"` · `fix N` · `resolve N --note "como"` · `duplicate N --of M` · `answer N intentional\|not-intentional` | Decisões humanas no modo local (`--executed-by agent --note` quando um agente executa a pedido de quem decidiu) |
| `sentinela linear members TIME` | Membros de um time do Linear: quem `fix_trigger: team_members` deixa disparar a correção |
| `sentinela finding report` | Regenera os relatórios do modo local |
| `sentinela status` | Serviços do systemd, modo, token do Linear (ou o MCP), repos sem AGENTS.md/CLAUDE.md, última rodada por repo, pendentes, fila (com motivo de espera), gatilhos consumidos pelo worker (ADR 0029), PRs abertos, chamadas ao MCP por ciclo e por dia, e o disco dos worktrees |
| `sentinela scan --repo R --estimate-only` | Só a estimativa de chamadas e custo, sem chamar modelos |
| `sentinela report --run N` | Relatório HTML da rodada: achados, decisões do Judge, custo por agente, scanners |
| `sentinela serve` | Sync com o Linear (polling ou webhook) e Feedback Curator semanal; `--once` faz um ciclo |
| `sentinela curate --repo R` | Roda o Feedback Curator agora |
| `sentinela worker` | Fila da linha de execução e acompanhamento dos PRs; `--once` faz um ciclo |
| `sentinela job list` · `show N` | Execuções e o detalhe de uma (reviews por rodada, motivos do gate, motivo de espera na fila; ADR 0031) |
| `sentinela job approve N --by NOME` | Uma pessoa aprova um job em ESCALATE (o PR continua em draft) |
| `sentinela job rework N "nota"` | Devolve ao builder com uma nota, na mesma branch e no mesmo PR |
| `sentinela job retry N` | Nova tentativa de um job que falhou ou foi bloqueado sem PR (pelo card: aplicar `sentinela-fix` de novo, ADR 0029). Falha por timeout ou contrato retoma a mesma sessão (limite de uso não é mais falha: o job espera o reset na fila, ADR 0048) (`claude --resume`); sem a sessão, ou em outra falha, recomeça do zero e grava o motivo (ADR 0030) |
| `sentinela job cancel N` | Cancela um job ainda na fila |
| `sentinela job sweep` | Remove agora os worktrees de PR mergeado ou fechado e os de falha além do prazo (ADR 0037) |
| `sentinela notes --execution N` | Notas fora do escopo dos reviewers → achados candidatos (`--publish` grava) |
| `sentinela rejudge --repo R [--only EX-10]` | Julga de novo achados já decididos com as regras atuais, sem gravar. Com `--apply`, grava como achado aberto o que estava descartado ou mesclado e agora publica ou vira pergunta (ADR 0054); os já gravados não mudam |
| `sentinela publish --repo R` | Publica no Linear os achados pendentes do repo numa rodada sem chamadas, sem analisar o código, com as regras do scan (limite por time, pergunta de produto). Exige `publish.enabled` |
| `sentinela metrics` | Bloqueios por lente, rodadas e custo por job |
| `sentinela config validate` | Confere a config contra Linear, GitHub, Slack, Apps e banco, e mostra o projeto, o canal e a menção efetivos com a fonte |
| `sentinela config validate --offline [--changed ARQUIVOS --base REF --codeowners REPO=ARQ]` | Estrutura, política e roteamento sem chamar ninguém; com `--changed`, o roteamento dos caminhos afetados antes e depois (ADR 0041) |
| `sentinela config schema [--team]` | JSON Schema do `sentinela.yaml` (ou do arquivo de time), gerado do código |
| `sentinela owners explain R CAMINHO` | Qual regra decidiu o dono de um caminho, e o projeto efetivo com a fonte |
| `sentinela owners pending` | Só leitura: o projeto de cada achado pendente de publicação (gravado, roteamento de hoje e o que a publicação usaria) |
| `sentinela config models --repo R --team T` | Modelo de cada papel e o nível do `sentinela.yaml` que o decidiu |
| `sentinela config bootstrap --repo R` | Rascunho de `teams` e `repos` a partir do CODEOWNERS |
| `sentinela migrate up` · `down` · `status` | Migrations |
| `sentinela docs guide` | Gera o guia interativo `docs/guia/index.html` a partir das fontes, dos docs, dos exemplos, do schema e da saída do binário |

## Rodando sozinho

Como o Sentinela roda sem ninguém no terminal. Os arquivos estão em `deploy/`, e o
`deploy/install.sh` gera e ativa as units.

| Peça | Como |
|---|---|
| Descoberta semanal | `sentinela-scan.timer`: segunda às 07:00 (até 10 min de atraso aleatório), `Persistent=true` (se a máquina estava desligada, roda ao ligar). Dispara `sentinela-scan.service`, que roda `scan --all` |
| Sync e curadoria | `sentinela-serve.service`, em loop, `Restart=always` |
| Linha de execução | `sentinela-worker.service`, em loop, `Restart=always` |
| Logs persistentes | journald (com `/var/log/journal`, os logs sobrevivem ao reboot): `journalctl -u sentinela-worker -f`, ou `journalctl --user ...` nas units de usuário |
| Reinício após reboot | units de sistema com `WantedBy=multi-user.target`; nas de usuário, `WantedBy=default.target` + `loginctl enable-linger` (sobem no boot, sem login). O Postgres já volta sozinho (`restart: unless-stopped` no compose) |
| Status | `sentinela status` |

**Por que systemd de usuário, e não um serviço no docker compose:** o worker e a descoberta
usam o Claude Code pela assinatura (login em `~/.claude`), o git com as credenciais do usuário,
os scanners instalados pelo mise e os worktrees em `~/.cache/sentinela`. Num contêiner, tudo isso
teria de ser montado ou reinstalado, e o login da assinatura ficaria dentro da imagem ou de um
volume. Rodar o builder e os reviewers num contêiner isolado é outro item do
[roadmap](roadmap.md), com outro objetivo: isolamento, e não operação.

**Arquivos:**

- `deploy/bin/sentinela-run`: wrapper que as units chamam. Ele entra no diretório do projeto,
  carrega o `.env` pelo shell (o `EnvironmentFile` do systemd não entende os comentários no fim da
  linha que o `.env` usa) e põe no PATH o `claude`, o Go do mise e o GOBIN (govulncheck,
  golangci-lint, gosec).
- `deploy/systemd/sentinela-{serve,worker,scan}.service` e `sentinela-scan.timer`: modelos, com
  `@SENTINELA_HOME@`, `@USER_LINE@` e `@WANTED_BY@` no lugar do caminho, do usuário e do alvo.
- `deploy/install.sh`: gera as units a partir dos modelos e liga tudo (`daemon-reload` e
  `enable --now`).
  - `sudo deploy/install.sh`: units de sistema em `/etc/systemd/system`, com `User=sentinela` e o
    Sentinela em `/opt/sentinela`. Troque com `SENTINELA_USER` e `SENTINELA_HOME`.
  - `deploy/install.sh --user`: units do usuário atual (`~/.config/systemd/user`), com o
    Sentinela no diretório do repo (ou `SENTINELA_HOME`) e `loginctl enable-linger`.
  - `deploy/install.sh --render DIR [--user]`: só gera as units em `DIR`, para conferir.
  - `deploy/install.sh --uninstall [--user]`: desfaz.
  - `--prefix NOME` (ou `SENTINELA_UNIT_PREFIX`): nome das units, com padrão `sentinela`. Gera
    `NOME-serve.service`, `NOME-worker.service`, `NOME-scan.service` e `NOME-scan.timer`.
  - O `install.sh` nunca sobrescreve nem remove a unit de outra instalação: se a unit com o mesmo
    nome existe e o `SENTINELA_HOME` dela é outro, para e pede outro `--prefix`.
  - Depois de mudar um modelo, rode o `install.sh` de novo: as units instaladas são arquivos
    gerados, não links.
- **Segunda instalação na mesma máquina (ADR 0056):**
  - **Instalação:** um clone do Sentinela em outro diretório, com porta e projeto do compose
    próprios (`SENTINELA_PG_PORT=55432 ./sentinela init ...`, ADR 0052).
  - **Units:** `deploy/install.sh --user --prefix <nome>` (ex.: `--prefix ceres-sentinela`).
  - **Diretórios:** worktrees, artefatos e o HOME do job ficam, por padrão, em
    `~/.cache/sentinela/<chave>/`, onde a chave é a da instalação (a mesma do
    `COMPOSE_PROJECT_NAME`).
  - **Instalação antiga:** se ela já tem execuções nos diretórios compartilhados de antes
    (`~/.cache/sentinela/worktrees`), os diretórios que não estão no `sentinela.yaml` continuam
    neles, com uma linha no log do worker.
  - **Status:** o `sentinela status` acha as units desta instalação pelo `SENTINELA_HOME`,
    qualquer que seja o prefixo.
- **Banco da instalação:** o banco guarda o diretório da instalação (`installation.home_dir`,
  ADR 0052). O `init`, o `serve` e o `worker` recusam o banco de outro diretório. Para mover a
  instalação de lugar, atualize o registro no banco (`UPDATE installation SET home_dir =
  '<novo diretório>'`) antes de subir os serviços no lugar novo.

**Antes de ativar (ou reativar depois de um `--uninstall`), confira:**

1. `go build -o sentinela ./cmd/sentinela` (as units usam o binário da raiz do projeto).
2. `./sentinela config validate` sem erros e `./sentinela linear whoami` mostrando o app.
3. O escopo do `scan --all` (ADR 0035): ele roda os repos já analisados, em modo incremental, e no
   máximo `discovery.max_new_repos_per_run` (padrão 1) repos nunca analisados por rodada, na ordem
   do `sentinela.yaml`. A primeira rodada de um repo novo é full. Com 0, só os já analisados. O
   `scan --all` para no limite de uso da assinatura, e o timer tenta de novo na semana seguinte.
4. `publish.enabled`: com `true`, a rodada semanal publica, respeitando o limite por time.

**Ativar e acompanhar:**

```bash
deploy/install.sh
./sentinela status
journalctl --user -u sentinela-scan --since today
systemctl --user list-timers sentinela-scan.timer
```

**Disco:** o worker remove sozinho o worktree de um job quando o PR é mergeado ou fechado, e o de
um job que falhou depois de `execution.failed_worktree_retention` (padrão 7 dias). Para limpar na
hora, use `sentinela job sweep`. O `sentinela status` mostra quanto os worktrees guardados ocupam e
quanto a limpeza já liberou (ADR 0037).

**Cota da assinatura (ADR 0048):** quando o Claude Code termina com limite de uso (semanal, de
sessão ou rate limit), o job volta para a fila. Fica com `wait_reason` "cota da assinatura até
<horário do reset>", sem `failed`, com a `sentinela-fix` na issue e sem comentário. O worker não
pega jobs até o reset (mais 5 minutos), e o PR Shepherd também espera. O canal padrão recebe um
aviso, uma vez. Depois do reset, o job continua a mesma sessão. O `sentinela status` e o log do
worker (`aguardando: job N ...`) mostram a espera. A descoberta termina a rodada como
`aborted_budget`, e a rodada seguinte analisa o mesmo código de novo.

**Publicação no Linear (ADR 0055):**

- **Falha numa issue:** vira aviso e a publicação segue com as outras. A issue que falhou fica
  pendente, e a próxima rodada tenta de novo.
- **Limite de uso da assinatura:** a publicação para na hora, e a rodada guarda o horário do reset
  (`stats.issues.paused_until`). Depois dele, o `serve` publica os pendentes do repo numa rodada
  sem chamadas, sem analisar o código de novo. O log diz `publicação de <repo>: retomando`.
- **No terminal:** o `scan` mostra o erro, quantos achados do repo continuam sem issue e como
  retomar, e sai com código diferente de 0.
- **Conector do claude.ai:** às vezes o `claude -p` começa sem as ferramentas do conector
  carregadas. A chamada ao MCP espera e tenta de novo (10 s e 30 s) antes de contar como falha.

**Parar:** `systemctl --user stop sentinela-worker`. Um job em andamento recebe SIGTERM e, em
120 s, SIGKILL; o retry retoma a sessão (ADR 0030). Para desligar tudo: `deploy/install.sh --uninstall`.

Sem systemd (CI ou outro agendador), os mesmos comandos servem:

```bash
deploy/bin/sentinela-run serve      # sync com o Linear e curadoria
deploy/bin/sentinela-run worker     # linha de execução
deploy/bin/sentinela-run scan --all # descoberta (agendada)
```

O `scan` é incremental: sem commits novos, termina sem chamadas (`no_changes`) e só publica os
achados pendentes.

## Decisões do time no Linear

O Sentinela lê estas decisões e reage. Quem decide é sempre uma pessoa.

| Ação no Linear | O que o Sentinela registra |
|---|---|
| Cancelar a issue com um comentário | Recusa: achado `rejected`, supressão com o motivo (o comentário); o achado não volta |
| Marcar como Duplicate de outra issue | Merge humano: o achado acompanha a original, sem supressão |
| Mover para Done | Aceito: achado `fixed` |
| Aplicar `sentinela-fix` | Pede a correção: o `worker` enfileira o job |
| (um job termina `failed` ou `blocked` sem PR) | O worker tira a `sentinela-fix`: o pedido foi atendido (ADR 0029) |
| Aplicar `sentinela-fix` de novo depois disso | Nova tentativa, como `sentinela job retry`; comente antes o que muda e o comentário entra na spec (ADR 0029) |
| (o worker abre o PR) | Registro de fato: a issue vai para `linear.review_state` (ex.: In Review), se configurado; issue concluída, cancelada ou duplicada não é mexida (ADR 0027) |
| Mergear o PR do Sentinela | Desfecho `merged` (`decided_by = human`), achado `fixed`; o worker move a issue para `linear.done_state` (padrão Done), salvo se já estiver concluída, cancelada ou duplicada (ADR 0028) |
| Comentar na issue antes de o job começar | Instrução da equipe: entra na spec e no PR com prioridade sobre a correção proposta pelo achado (ADR 0026) |
| Em uma pergunta, aplicar `sentinela-nao-intencional` | A pergunta vira o achado normal, na mesma issue; desfecho `accepted` (`decided_by = human`, com quem aplicou a label quando o tracker sabe) |
| Em uma pergunta, aplicar `sentinela-intencional` (e comentar o motivo) | Recusa com supressão e o motivo; a issue fica para o time fechar |

Comentários escritos pelo Sentinela começam com `**Sentinela` e nunca contam como motivo de decisão
nem como instrução. Para ajustar a correção antes da primeira rodada, comente na issue antes de o
`worker` pegar o job (com ou sem a label já aplicada). Num job seguinte do mesmo achado, entram só
os comentários feitos depois de o anterior começar. O registro de quais comentários entraram em cada
job fica em `executions.artifacts` (`team-instructions`).

## PRs da linha de execução

- Sempre em draft, na branch `sentinela/<issue>`, com `Fixes <issue>` e o resumo de spec, build,
  reviews e decisão.
- A app revisora publica uma review COMMENT com a decisão do approver e do Policy Gate. Ela é
  reavaliada quando o CI termina.
- CI vermelho ou review humana pedindo mudança acionam o PR Shepherd, com até 3 ciclos.
- Merge, fechamento e revert viram desfechos do achado. Um revert entra no histórico do módulo, e o
  gate passa a pedir humano para esse módulo.
- O merge é sempre de uma pessoa.

## Diagnóstico

| Sintoma | Causa provável |
|---|---|
| Scanner `pulado (... não está no PATH)` | Instale a ferramenta ou tire-a de `discovery.scanners` |
| Scanner `falhou (... saída: ...)` | A saída da ferramenta está no detalhe; ela rodou e falhou, ou não analisou nada |
| Rodada `aborted_budget` | Orçamento em dólar (API) ou limite de chamadas ou de uso da assinatura |
| `claude: chave de API no ambiente` | Uma sessão do Claude Code começaria com chave de API; confira o ambiente |
| `instruções de outro projeto no caminho` | Há um `CLAUDE.md` acima do diretório de trabalho; mova `workdir` ou `worktrees` |
| Job `failed`: "não tem AGENTS.md nem CLAUDE.md" | O repo-alvo precisa dos comandos de build e teste na raiz ou no workspace do achado (ADR 0059). O `config validate` (pelo GitHub) e o `status` (pelo clone do último scan) listam os repos sem o arquivo, e a issue publicada num deles traz uma linha avisando |
| Job `failed`: "... começa com ..." | Um agente quebrou o contrato do arquivo; veja `~/.cache/sentinela/jobs/N/` |
| Aviso `not_in_channel` | Convide o bot no canal (o canal padrão recebe o aviso) |
| Issue não publicada, "label ... não existe" | Crie as labels do `sentinela.yaml` no time do Linear |
| Log `issue com o marcador não reaproveitada` | Há no time uma issue com o mesmo fingerprint, mas arquivada, na lixeira, de outro projeto ou de outra instalação (ADR 0047); a publicação cria outra |
| Achado ligado à issue errada | `sentinela finding unlink <id> --issue <ID> --reason "<motivo>"`: o achado volta a pendente e a ligação antiga fica em `finding_unlinks` |

Cada job guarda em `~/.cache/sentinela/jobs/N/`:
- `job.json`, `issue.md`, a spec, os artefatos de cada rodada e a decisão;
- o stream bruto da sessão (`stream.jsonl`).

## Banco

```bash
docker compose exec postgres pg_dump -U sentinela sentinela > backup.sql
```

As tabelas que importam a longo prazo são `outcomes`, `suppressions`, `feedback_examples` e
`thresholds`: é o que o time ensinou. As outras podem ser reconstruídas por novas rodadas.

### pgAdmin

O compose traz um pgAdmin já conectado ao banco, no profile `tools`. Ele não sobe com o
`docker compose up` padrão.

```bash
docker compose --profile tools up -d           # sobe o Postgres (se preciso) e o pgAdmin
# abra http://127.0.0.1:5050
docker compose --profile tools stop pgadmin    # para só o pgAdmin
docker compose --profile tools down            # para tudo (os volumes ficam)
```

- **Acesso:** sem tela de login nem senha mestra (modo desktop) e com a porta só em `127.0.0.1`.
  A porta muda com `SENTINELA_PGADMIN_PORT`.
- **Servidores:** o grupo "Sentinela" já vem com dois, `sentinela` e `sentinela_test`, cadastrados
  por `docker/pgadmin/servers.json`. A senha vem de `docker/pgadmin/pgpass`. As tabelas ficam em
  Databases > sentinela > Schemas > public > Tables.
- **Estado:** o estado do pgAdmin (preferências, consultas salvas) fica no volume `pgadmin`. Os
  servidores são cadastrados só na primeira subida; para recarregá-los depois de mudar o
  `servers.json`, apague o volume com `docker compose --profile tools down` e depois
  `docker volume rm sentinela_pgadmin`.
- **`sentinela_test`:** aparece vazio até a primeira execução dos testes de integração. Se o banco
  não existir, crie com o `createdb` do [README](../README.md#desenvolvimento).

As credenciais do Postgres no compose (`sentinela`/`sentinela`) são só para desenvolvimento local.
Não exponha nem o Postgres nem o pgAdmin fora da máquina.
