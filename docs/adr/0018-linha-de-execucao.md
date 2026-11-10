# ADR 0018: Linha de execução (Fase 4) com travas para as primeiras execuções

- Status: aceita
- Data: 2026-10-10

## Decisão

### Gatilho e fila
- **Gatilho:**
  - A label `sentinela-fix` só é aplicada por um humano (princípio 6). O worker lê as issues
    principais com achado `open` e sem execução não cancelada (`TriggerCandidates`). Enfileira
    as que têm a label e não estão canceladas, duplicadas nem concluídas.
  - O mesmo achado não volta a ser enfileirado pela label: uma nova tentativa é
    `sentinela job rework`.
- **Fila:**
  - `executions` em Postgres. O claim usa `FOR UPDATE SKIP LOCKED` (e, desde a ADR 0031, respeita conflitos de arquivos).
  - O índice parcial `executions_one_active_idx` garante no máximo um job ativo por achado.
  - `sentinela worker` roda `execution.concurrency` jobs em paralelo.

### Workspace do job
- **Worktree e branch:** um git worktree por job, na branch `sentinela/<issue>`, criado a partir
  de `origin/<padrão>`. O clone de leitura fica em `discovery.workdir`.
- **Diretório do job:** recebe `job.json`, `issue.md`, os artefatos, o stream-json bruto e uma
  cópia do plugin.
- **Local:** worktrees e jobs ficam em `~/.cache/sentinela/`, fora de qualquer projeto. O Claude
  Code carrega o `CLAUDE.md` do diretório atual e de todos os pais, e um worktree dentro deste
  repo levaria aos agentes as instruções do Sentinela. O worker falha o job se achar `CLAUDE.md`
  acima do worktree.
- **Repo sem `AGENTS.md` nem `CLAUDE.md` na raiz:** o job falha com uma mensagem que diz o que
  falta. Os agentes não carregam nada específico de repo.

### Plugin `sentinela-factory`
- **Composição:**
  - a skill `sentinela-fix`, que orquestra;
  - os sete subagentes: spec-writer, builder, quatro reviewers e approver;
  - a skill `sentinela-shepherd`.
- **Invocação:**
  - O worker invoca `claude -p "/sentinela-factory:sentinela-fix <JOB>" --plugin-dir <JOB>/plugin
    --output-format stream-json --verbose`.
  - Na mesma linha vão `--add-dir <JOB>`, `--permission-mode acceptEdits`, `--allowedTools` e
    `--disallowedTools` (push, gh, curl, wget, WebFetch e WebSearch). O `--model` é o do
    orquestrador e o `--max-budget-usd` é `execution.max_budget_usd`.
  - As flags foram conferidas em `claude --help` (2.1.296).
  - O plugin é carregado só para a sessão; nada é copiado para o repo-alvo.
- **Modelos:** o worker reescreve o `model:` de cada subagente com o papel correspondente do
  `sentinela.yaml`, que é a fonte da verdade.
- **Mecânica conferida num probe real com Haiku:**
  - a skill é chamada como `/<plugin>:<skill>`;
  - o subagente respeita o `model` do frontmatter;
  - o mesmo subagente é retomado com SendMessage e lembra do que fez. É assim que a skill
    retoma o mesmo builder nas rodadas 2 e 3.
- **Spec:** quando a issue veio do Judge com critérios de aceite, o worker escreve `spec.md` (com
  `SPEC: READY`) direto do achado, e a skill pula o spec-writer.
- **Contratos:**
  - Cada agente escreve um arquivo, e a primeira linha é o veredito.
  - Só a skill escreve `state.json`.
  - Depois da sessão, `executor.Collect` confere tudo em código: arquivos de cada rodada,
    vereditos, coerência entre `state.json` e os arquivos. Rodadas esgotadas viram ESCALATE sem
    approver.
  - Qualquer divergência vira `failed`, com o motivo.

### PR, gate e revisão
- **PR:**
  - O push usa o token de instalação da app bot e só aceita branches `sentinela/*`, nunca a
    padrão (`repo.Git.Push`).
  - O PR é aberto sempre em draft; `github.CreatePR` recusa outro modo.
  - O corpo começa com `Fixes <ID>` e traz spec, build, reviews e decisão resumidos.
- **Policy Gate (`internal/gate`, código puro):** precisa de humano se ocorrer qualquer um destes
  casos:
  - o approver não deu APPROVE (a menos que `job approve`);
  - o time tem `blocked`, ou `auto_approve` desligado ou ausente;
  - a categoria é security;
  - linhas ou arquivos passam do limite do time;
  - há arquivo em path bloqueado ou fora de `scope_paths`;
  - o CI não está verde;
  - o módulo tem revert.
- **Trava dupla da revisora:**
  - Ela publica review COMMENT com a decisão do approver e do gate.
  - Só publica APPROVE se o gate aprovar e `github_apps.reviewer_approves: true`, que está
    desligado.
  - `policy.auto_approve` está `false` em todos os times, inclusive plataforma.
  - Nenhum código faz merge.
- **Labels e avisos:**
  - O Sentinela aplica no Linear só as labels de resultado (`sentinela-done`,
    `sentinela-failed`), nunca a de gatilho. Comenta na issue e avisa o canal do time.

### Acompanhamento
- **Ciclo:** `Worker.FollowUp` roda a cada ciclo do worker e no webhook do GitHub. Ele trata:
  - **Merge:** outcome `merged`, achado `fixed`.
  - **Fechamento sem merge:** `closed`, achado volta a `open`.
  - **Revert:** um commit "This reverts commit <merge>" na branch padrão vira outcome `reverted`
    e achado `reverted`; entra no histórico do gate.
  - **CI vermelho e review humana pedindo mudança:** acionam o PR Shepherd, no máximo 3 ciclos
    por PR, com cada evento tratado uma vez. Esgotado, o Sentinela para e avisa.
  - **CI verde:** o gate é reavaliado e a revisora comenta de novo, uma vez por commit.
- **Webhook do GitHub:** `POST /webhooks/github` no `serve`, em modo webhook. Valida
  `X-Hub-Signature-256`, deduplica por `X-GitHub-Delivery` e só liga com `GITHUB_WEBHOOK_SECRET`.
  Sem segredo, vale o polling do worker.
- **Comandos de humano:**
  - `job approve <id> --by`: só ESCALATE, uma vez. Reavalia o gate e a revisora comenta.
  - `job rework <id> "<nota>"`: novo job na mesma branch e no mesmo PR, com rodadas zeradas, a
    nota e os artefatos do job anterior em `previous/`.
  - `job list`, `job show` e `job cancel`.
- **Métricas:**
  - `sentinela metrics` mostra reviews, bloqueios e jobs barrados por lente.
  - Mostra também os bloqueios "confirmados pelo humano": o humano pediu mudança no PR ou o
    fechou sem merge.
  - Mostra rodadas e custo por job. O custo vem do `result` do stream-json, é o equivalente em
    preço de lista e fica gravado em `llm_calls`, uma linha por modelo.

## Esquema
Migration `00010`: colunas novas em `executions` (tipo, issue, worktree, decisão, gate, PR, shepherd,
artefatos) e a tabela `reviews` (execução, rodada, lente, veredito, achados). A spec da Fase 4 pede a
tabela `reviews`; a mudança veio com a ordem de executar a fase.

## Fora (por enquanto)
- Auto-merge por categoria: não implementado. A ordem é "nenhum merge".
- "Confirmado pelo humano" é aproximado (review pedindo mudança ou PR fechado sem merge); falta
  cruzar com o conteúdo da review humana.
- O shepherd não faz rebase quando a branch padrão anda; um conflito vira CI vermelho ou review
  humana.
