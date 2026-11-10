# Integrações

Esta página é o modo completo (`adapters.tracker: linear_api`, `notifier: slack_api`, `git_host:
github_apps`). Nos modos local e MCP, Linear, Slack e GitHub Apps não são necessários, ou são
trocados pelos MCPs do Claude Code e pelo `gh`: veja [modos.md](modos.md).

O Sentinela fala com quatro serviços. Só o Claude é obrigatório; os outros entram quando você
liga a parte correspondente.

| Serviço | Para quê | Quando é preciso |
|---|---|---|
| Claude (assinatura ou API) | todos os agentes | sempre |
| GitHub (token de leitura) | clonar, ler CODEOWNERS, conferir times | sempre |
| Linear | publicar issues e ler as decisões | `publish.enabled: true` e `serve` |
| Slack | resumo de cada rodada e avisos por time | opcional |
| GitHub Apps (bot e revisora) | push, PR em draft, revisão | `worker` (Fase 4) |

Todo segredo vai no `.env` (veja `.env.example`), nunca no `sentinela.yaml`. Depois de cada
integração, rode `./sentinela config validate`: ele confere de verdade o que estiver configurado.

## Claude

### Pela assinatura (`llm.provider: claude_code`)

Os agentes chamam o Claude Code em modo não interativo (`claude -p`), com a sua assinatura.

1. Instale o Claude Code e faça login com a conta da assinatura (`claude`, depois `/login`).
2. No `sentinela.yaml`: `llm.provider: claude_code`.
3. Confira com `claude auth status`: `authMethod` deve ser `claude.ai`.

Cuidados:
- O Sentinela tira todas as variáveis `ANTHROPIC_*` do ambiente antes de chamar o `claude`. Com
  `ANTHROPIC_API_KEY` presente, o Claude Code usaria crédito da API em vez da assinatura. Se mesmo
  assim uma sessão começar com chave de API, o processo é encerrado antes da primeira chamada.
- O limite da rodada é em chamadas (`llm.claude_code.max_calls_per_run`), não em dólar. Se a
  assinatura bater no limite de uso, a rodada para com `aborted_budget`.
- Os agentes rodam em `llm.claude_code.workdir`, fora de qualquer projeto. O Claude Code carrega o
  `CLAUDE.md` do diretório atual e de todos os pais, e instruções de outro projeto contaminariam o
  julgamento.

### Pela API (`llm.provider: anthropic_api`)

1. Crie uma chave no Console da Anthropic e coloque em `ANTHROPIC_API_KEY`. Se a chave não estiver
   presa a um workspace, preencha também `ANTHROPIC_WORKSPACE_ID`.
2. `llm.provider: anthropic_api`. Com `models.use_batch: true`, a descoberta usa a Batch API (50% do
   preço). Prompt caching vale no contexto comum.
3. O orçamento é em dólar (`budget.per_run_usd`). A rodada estima o custo antes de chamar qualquer
   modelo e para se o valor esperado passar do orçamento.

A linha de execução (Fase 4) usa sempre o Claude Code, porque o plugin `sentinela-factory` é do
Claude Code. Com `anthropic_api`, só a descoberta usa a API.

### Respostas gravadas (`llm.provider: replay`)

Com `llm.claude_code.record_dir` definido, cada resposta válida é gravada. Depois,
`llm.provider: replay` e `llm.replay.dir` reproduzem a rodada sem chamar nenhum modelo, o que é útil
para demonstrações e depuração.

## GitHub: token de leitura

`GITHUB_TOKEN` serve para clonar repos privados, ler o CODEOWNERS e conferir se times e usuários
existem. Use um token fine-grained só de leitura:

| Permissão | Nível |
|---|---|
| Contents | Read |
| Metadata | Read |
| Members (da organização, para conferir times) | Read |

## Linear

1. Crie um app OAuth para o Sentinela (Settings > API > OAuth applications), com a callback
   `http://localhost:8787/oauth/linear/callback`, e coloque o client ID e o secret em
   `LINEAR_OAUTH_CLIENT_ID` e `LINEAR_OAUTH_CLIENT_SECRET`.
2. Rode `./sentinela linear auth`. O comando sobe um servidor temporário na porta da callback e
   mostra o link de autorização, com `actor=app`. Um admin do workspace autoriza no navegador. O
   token e o refresh token ficam em `linear_auth.token_file` (padrão
   `~/.config/sentinela/linear-token.json`, permissão 0600, fora do git) e são renovados sozinhos
   (o access token dura 24 h e o refresh token gira a cada uso). Com
   `linear_auth.grant: client_credentials`, habilitado no app, não há navegador: o token é de app e
   dura cerca de 30 dias.
3. `./sentinela linear whoami` mostra a identidade, e `./sentinela linear test-comment <ISSUE>`
   comenta numa issue e confere que o comentário saiu como o app.
4. No time de cada board, crie as labels de `labels:` no `sentinela.yaml`: `sentinela`,
   `sentinela-fix`, `sentinela-done`, `sentinela-failed`, `sentinela-pergunta`,
   `sentinela-intencional` e `sentinela-nao-intencional`.
5. Confira os estados: `triage_state` precisa existir no time (Triage, se habilitado; senão Backlog,
   por exemplo).
6. Crie os projetos citados em `teams[].linear.project` e `repos[].linear_project`.
7. `./sentinela config validate`: aponta time, projeto, estado ou label que faltar e diz com que
   identidade as ações saem.

Com o app, issues, comentários, labels e mudanças de estado saem como o app, e o sync reconhece o
Sentinela pelo autor (ADR 0032). O prefixo `**Sentinela` continua nos comentários, mas só como
texto. Sem o app (`linear_auth.mode: api_key`, ou `fallback_api_key: true` com o token
indisponível), a chave pessoal `LINEAR_API_KEY` volta a valer: tudo sai com o nome da pessoa dona
dela, e o prefixo volta a ser o que separa o Sentinela.

### Polling ou webhook

- **Polling** (`trigger.mode: polling`, padrão): `sentinela serve` lê o estado das issues
  publicadas a cada `trigger.polling_interval`. Não precisa de endereço público.
- **Webhook** (`trigger.mode: webhook`): crie um webhook no Linear apontando para
  `http://<seu-host>:<porta>/webhooks/linear`, com eventos de Issue, e coloque o segredo em
  `LINEAR_WEBHOOK_SECRET`. O Sentinela valida a assinatura (`Linear-Signature`), recusa eventos com
  mais de 1 minuto e deduplica pela entrega.

## Slack

1. Crie uma app a partir de [docs/manifests/slack-app-manifest.yaml](manifests/slack-app-manifest.yaml)
   (api.slack.com/apps > Create New App > From a manifest). Escopos: `chat:write`,
   `chat:write.public` e `channels:read`.
2. Instale no workspace e coloque o Bot User OAuth Token (`xoxb-...`) em `SLACK_BOT_TOKEN`.
3. Convide o bot em cada canal configurado (`defaults.notify.slack` e `teams[].notify.slack`):
   `/invite @Sentinela`, ou Integrações > Adicionar apps no canal.
4. Use o ID do canal (`C...`), não o nome: o nome é resolvido uma vez, mas o ID não muda.
5. `./sentinela config validate`: confere que cada canal existe, não está arquivado e tem o bot.

Sem `SLACK_BOT_TOKEN`, o Sentinela roda normalmente, só que sem avisos. Erro de postagem num canal
nunca derruba a rodada: vira aviso, e o canal padrão recebe a explicação.

## GitHub Apps (Fase 4)

A linha de execução usa duas apps com permissões separadas:

| App | Permissões | O que faz |
|---|---|---|
| bot (`github_apps.bot`) | Contents: write, Pull requests: write, Metadata: read, Checks: read, Commit statuses: read | push na branch `sentinela/<issue>` e PR em draft |
| revisora (`github_apps.reviewer`) | Pull requests: write, Contents: read, Metadata: read, Checks: read, Commit statuses: read | review COMMENT com a decisão do approver e do gate |

1. Crie as duas a partir de [docs/manifests/github-app-bot.json](manifests/github-app-bot.json) e
   [docs/manifests/github-app-reviewer.json](manifests/github-app-reviewer.json) (Settings > Developer
   settings > GitHub Apps > New). Troque `<sua-org>` no nome.
2. Instale cada uma só nos repos do `sentinela.yaml` ("Only select repositories").
3. Gere uma chave privada para cada uma, salve em `secrets/` (fora do git) e restrinja o acesso:
   `chmod 600 secrets/*.pem`.
4. Preencha no `.env`: `GITHUB_BOT_APP_ID`, `GITHUB_BOT_INSTALLATION_ID`,
   `GITHUB_BOT_PRIVATE_KEY_PATH` e os equivalentes `GITHUB_REVIEWER_*`.
5. Coloque os slugs em `github_apps.bot` e `github_apps.reviewer`.
6. `./sentinela config validate`: gera o token de instalação de cada app e confere:
   - o slug;
   - as permissões efetivas: falta é erro, e permissão demais (por exemplo, a revisora com
     Contents: write) também;
   - o acesso a cada repo;
   - se a app está instalada em todos os repos da conta, o que gera um aviso.

A revisora só publica APPROVE com `github_apps.reviewer_approves: true` **e** a aprovação do Policy
Gate. O padrão é `false`: ela só comenta.

### Webhook do GitHub (opcional)

Com `trigger.mode: webhook` e `GITHUB_WEBHOOK_SECRET`, o `serve` recebe
`POST /webhooks/github` (eventos `pull_request`, `pull_request_review`, `check_suite`,
`check_run`). Ele valida `X-Hub-Signature-256` e deduplica por `X-GitHub-Delivery`. Sem o webhook,
o `worker` acompanha os PRs por polling.

## Repositório-alvo

Para a linha de execução, cada repo precisa de um `AGENTS.md` (ou `CLAUDE.md`) com os comandos de
setup, build, teste e lint. Os agentes não carregam nada específico de repo; sem esse arquivo, o job
falha com uma mensagem dizendo o que falta. Num monorepo, cada workspace pode ter o seu
(`apps/bff/AGENTS.md`): o job usa o mais próximo do escopo do achado e o da raiz, com o setup comum,
como reserva (ADR 0059). Modelos para TypeScript com npm, pnpm e yarn estão em
[agents-md-typescript.md](agents-md-typescript.md).
