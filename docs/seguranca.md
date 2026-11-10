# Segurança

## Segredos

- Todo segredo fica no `.env` (fora do git): chaves do Linear, Slack, GitHub e Anthropic, e os
  segredos de webhook. O `sentinela.yaml` não tem segredos e também fica fora do git, porque
  descreve a sua organização.
- As chaves privadas das GitHub Apps ficam em `secrets/` (fora do git e da imagem Docker), com
  `chmod 600`.
- `.gitignore` e `.dockerignore` excluem `.env`, `secrets/` e `sentinela.yaml`.
- O Sentinela nunca imprime o valor de um segredo. Tokens de instalação das Apps vivem só em memória
  e são renovados antes de expirar.
- Os clones usam o token por cabeçalho HTTP, nunca na URL do remoto.

## Permissões mínimas

| Credencial | Permissões |
|---|---|
| `GITHUB_TOKEN` | Contents: read, Metadata: read, Members: read |
| App bot | Contents: write, Pull requests: write, Metadata: read, Checks: read, Commit statuses: read |
| App revisora | Pull requests: write, Contents: read, Metadata: read, Checks: read, Commit statuses: read |
| Slack | `chat:write`, `chat:write.public`, `channels:read` |
| Linear | chave de um usuário com acesso aos times configurados |

O `config validate` recusa a app revisora com Contents: write e qualquer uma das duas com
Administration ou Workflows: write. Também avisa se uma app está instalada em todos os repos da
conta: instale só nos repos do `sentinela.yaml`.

## Linha de execução

### Runtime do job: host ou contêiner

O Claude Code do job roda guiado por conteúdo de fora (o código do repo, os comentários da issue,
as reviews e o CI). Há dois lugares para ele rodar (`execution.runtime`, ADR 0053):

| O job alcança | `host` (padrão) | `container` (recomendado para empresa) |
|---|---|---|
| `.env`, `secrets/` e `~/.config/sentinela` do Sentinela | **Sim**, por caminho absoluto (é o mesmo usuário) | Não: não são montados |
| `~/.ssh` e o ssh-agent do usuário | **Sim**, por caminho absoluto e pelo socket do agent | Não |
| Docker do host | **Sim**, se o usuário do worker estiver no grupo `docker` | Não: o socket não é montado; com `job_docker: dind`, um Docker isolado só do job |
| Rede | **Livre** (só `curl` e `wget` negados no Bash) | Só os destinos de `execution.container.egress_allow`, pelo proxy de saída; o resto recebe 403 |
| Conectores claude.ai da conta (MCP do Linear, do Slack...) | Sim, pela conta logada | Não: o `mcp-proxy.anthropic.com` fica fora da lista |
| Git, ssh e gh com credencial | Não (HOME próprio, ADR 0049) | Não (sem credencial e sem `gh` na imagem) |
| Worktree, clone e diretório do job | Sim | Sim: são montados (o builder commita e escreve os artefatos) |

**No runtime `container`:**

- **Montagens:** o worker monta só o worktree, o diretório git comum do clone, o diretório do job
  e o HOME do job com a cópia do login do Claude Code (0600).
- **Processo:** roda com o uid do worker, sem capabilities e com `no-new-privileges`.
- **Rede:** cada job tem uma rede interna sem rota para fora; a única saída é o proxy
  `sentinela egress-proxy`, com a lista permitida. O log do proxy fica em `egress.log`, no
  diretório do job.
- **Ações externas:** push, PR, comentários e labels continuam no worker, no host, que é quem tem
  os segredos.
- **Limpeza:** no fim de cada sessão, contêineres, volumes e rede são removidos pelo nome que a
  sessão criou.

**No runtime `host`:** valem a lista fechada de ambiente e o HOME próprio abaixo, mas é o mesmo
usuário do worker. Nesse caso, rode o worker com um usuário de serviço sem acesso aos arquivos do
Sentinela e fora do grupo `docker`.

- **Isolamento:** cada job roda num git worktree próprio, fora de qualquer projeto. Os agentes não
  carregam instruções de outro repositório: o worker recusa o job se houver `CLAUDE.md` acima do
  worktree.
- **Ferramentas negadas ao Claude Code:** `git push`, `gh`, `curl`, `wget`, WebFetch e WebSearch
  (`execution.disallowed_tools`). O push é feito pelo worker, com o token da app bot.
- **Push:** só em branches `sentinela/*`, nunca na branch padrão. O PR é sempre aberto em draft; o
  cliente recusa outro modo.
- **Paths bloqueados** (`blocked_paths`, por exemplo pagamento, migrations, autenticação, CI): o
  builder é instruído a não mexer neles e o Policy Gate nunca auto-aprova um diff que os toque. O
  mesmo vale para times com `policy.blocked: true`.
- **O approver escala** (ESCALATE) qualquer mudança que toque em:
  - dinheiro, autenticação ou permissões;
  - CI ou deploy, migrations;
  - dependência nova, dados pessoais;
  - premissa de produto.
- **Revisora:** só publica APPROVE com `github_apps.reviewer_approves: true` **e** a aprovação do
  gate. O padrão é só comentar. Não há merge automático.
- **Ambiente do job: lista fechada.** O Claude Code do job roda Bash no worktree, com conteúdo que
  vem de fora: o código do repo, os comentários da issue, o texto das reviews e as anotações do CI.
  Por isso, ele não herda o ambiente do Sentinela (o `.env` é carregado no processo do worker).
  - **O que passa:** só `PATH`, `HOME`, `USER`, `LOGNAME`, `SHELL`, `TERM`, `TMPDIR`, `TZ`, o
    idioma (`LANG`, `LANGUAGE`, `LC_*`) e o necessário para o login do Claude Code
    (`CLAUDE_CONFIG_DIR`, `CLAUDE_CODE_OAUTH_TOKEN` e os `XDG_*`).
  - **O que fica de fora:** tudo o mais, inclusive `GITHUB_TOKEN`, `GH_TOKEN`, `SLACK_BOT_TOKEN`, as
    variáveis `LINEAR_*`, as das GitHub Apps (com o caminho das chaves privadas), os segredos de
    webhook, `DATABASE_URL` e `ANTHROPIC_*`.
  - **A lista está em código** (`internal/llm/env.go`) e o teste
    `TestJobProcessGetsOnlyTheAllowedEnvironment` confere o ambiente do processo filho.
  - **Vale para todo `claude -p`:** a descoberta (`TestClaudeCodeGetsOnlyTheClosedEnvironment`) e
    as chamadas aos MCPs do Linear e do Slack recebem a mesma lista (D-17 da revisão de
    prontidão). Antes, elas herdavam o ambiente inteiro menos `ANTHROPIC_*`.
  - **Repo que precisa de outra variável para os testes:** use os padrões do toolchain (Go, Gradle,
    npm e afins usam `HOME`) ou diga no `AGENTS.md` do repo como o comando a obtém. Nunca ponha
    segredo nessa lista.
  - **O que a lista não cobre, no runtime `host`:** o job ainda pode ler arquivos da máquina pelo
    `Read` e pelo Bash (por exemplo `~/.config/sentinela` e `secrets/`). Use o runtime `container`
    (acima) ou um usuário de serviço sem acesso a esses arquivos.
  - **Credenciais de git da máquina:** o Claude Code do job roda com um HOME do Sentinela
    (`execution.home`, ADR 0049), que tem só o login do Claude Code. O git roda sem configuração
    global nem de sistema, sem prompt e sem ssh, e o gh não acha login nem chaveiro. De dentro do
    job, um push não autentica pelo credential helper do `~/.gitconfig`, pela chave em `~/.ssh` nem
    pelo `gh`. **O que isso não fecha:** é o mesmo usuário. Um comando que leia a chave pelo caminho
    absoluto ainda chega a ela. A defesa completa é a proteção da branch padrão no GitHub e o
    usuário de serviço acima.
  - **Arquivos do registro privado (`execution.home_files`, ADR 0057):** a lista leva para o HOME
    do job arquivos de configuração de registro, como `.npmrc`, `.gradle/gradle.properties` e
    `.m2/settings.xml`. Eles são copiados com 0600 antes de cada job e somem quando saem da lista.
    **O job lê esses arquivos**, e o que eles têm pode sair pela rede ou num commit. Use só token
    de leitura do registro, nunca senha pessoal nem token com escrita, e só quando o build do repo
    precisar. A config recusa caminhos de credencial da máquina (`.ssh`, `.gitconfig`,
    `.config/gh`, `.config/sentinela`, `.claude`, `.docker`, `.aws` e afins).
  - **Ambiente de build:** o job recebe o `JAVA_HOME` do worker e
    `GRADLE_OPTS=-Dorg.gradle.daemon=false` (o daemon do Gradle sobreviveria ao fim do job). O
    `GRADLE_OPTS` do worker não entra, porque pode levar senha de proxy.
  - **Chave SSH da máquina com senha:** a chave em `~/.ssh` deve ter senha
    (`ssh-keygen -p -f ~/.ssh/id_ed25519`).
    - **Por que isso fecha o arquivo:** o job roda com o mesmo usuário e consegue ler a chave pelo
      caminho absoluto. Com senha, o arquivo lido não serve para autenticar sem a senha, e o job
      não tem como pedi-la: roda sem terminal e sem stdin, com `GIT_TERMINAL_PROMPT=0`, sem
      `SSH_ASKPASS` e sem `DISPLAY` (a lista fechada do ambiente não os passa). Sem senha, ler o
      arquivo basta para dar push com a sua conta em qualquer repo seu.
    - **O que a senha não fecha: o ssh-agent.** Depois que você destrava a chave na sessão, ela
      fica no agent, e o socket dele tem caminho previsível (no GNOME, `/run/user/<uid>/gcr/ssh`).
      O job não recebe `SSH_AUTH_SOCK`, mas pode defini-lo sozinho. Conferido nesta máquina em
      2026-10-16: com o ambiente do job e `SSH_AUTH_SOCK` apontado para esse caminho, o
      `ssh-add -l` lista a chave.
    - **Para fechar também o agent:** não deixe a chave carregada enquanto o worker roda, ou
      carregue-a com prazo (`ssh-add -t 1h`). No GNOME, não marque "destravar automaticamente ao
      entrar": com essa opção, o chaveiro guarda a senha, e o agent volta a ter a chave sem
      perguntar.
    - **Estado em 2026-10-16:** a `~/.ssh/id_ed25519` desta máquina ainda está **sem senha** e
      carregada no agent do GNOME. O Sentinela não mexe na chave.
  - **Ruleset na branch padrão:** os 11 repos do `sentinela.yaml` têm, desde 2026-10-16, o ruleset
    "main: sem force push nem deleção" (alvo `~DEFAULT_BRANCH`, regras `non_fast_forward` e
    `deletion`, sem bypass). Ele não exige PR: o push direto na `main` continua permitido, e um
    push vindo do job também passaria. O que o ruleset garante é que nenhum push reescreva nem
    apague o histórico da `main`, então qualquer commit indevido fica visível e se reverte. Repo
    novo no `sentinela.yaml` precisa do mesmo ruleset (`gh api -X POST repos/<repo>/rulesets`,
    corpo no relatório de 2026-10-16).
- **Assinatura:** além de não passar `ANTHROPIC_*`, o worker encerra a sessão se ela começar com
  chave de API.
- **osv-scanner (ADR 0058):** manda os nomes e as versões das dependências dos lockfiles e
  manifestos para a API do OSV (osv.dev), e, para `pom.xml`, consulta o Maven Central ao resolver
  dependências. Não envia código. Num ambiente fechado, deixe o `osv-scanner` fora de
  `discovery.scanners`, ou aponte o `--maven-registry` para o registro interno.
- **eslint e tsc (ADR 0060):** vêm de `tools/node`, instalado com `--ignore-scripts`. Não carregam
  a config do repo (o `eslint.config.js` dele é JavaScript do repo) nem instalam as dependências
  dele (o `postinstall` seria código de terceiros). Só leem os arquivos. O `tsc` só roda onde o
  `node_modules` já existe, e não grava nada no checkout.
- **Scanners da descoberta:** golangci-lint, gosec, govulncheck, semgrep, osv-scanner, eslint, tsc e o `go list` rodam no
  clone do repo-alvo sem as variáveis de segredo do Sentinela: prefixos `GITHUB_`, `GH_`, `SLACK_`,
  `LINEAR_`, `ANTHROPIC_`, `SENTRY_`, `DD_`, `SENTINELA_`, `DATABASE_URL` e qualquer nome terminado
  em `_TOKEN`, `_SECRET`, `_KEY` ou `_PASSWORD` (`internal/scan/env.go`). O resto do ambiente
  passa, para as ferramentas funcionarem como antes.
- **`GOTOOLCHAIN` fica no padrão (`auto`), desde 2026-10-16:** um `go.mod` que
  peça um Go mais novo faz o `go` dos scanners e do job baixar esse toolchain.
  - **Por que não `local`:** com `local`, um repo que subisse a diretiva `go` (o achado 31 propõe
    `go 1.27.2`) faria os scanners e os testes do job falharem nele até alguém atualizar o Go da
    máquina. A falha seria silenciosa no scan de segunda.
  - **O risco aceito é pequeno:** o toolchain vem do proxy oficial (`proxy.golang.org`) e é
    conferido no `sum.golang.org`, como qualquer módulo. Um repo-alvo só escolhe a versão; não
    consegue apontar um binário arbitrário enquanto `GOPROXY`, `GONOSUMDB` e `GOFLAGS` da máquina
    não mudarem, e o `go.mod` não muda nenhum deles.
  - **Para revisar:** a garantia depende da configuração do Go da máquina. Se `GOPROXY`,
    `GONOSUMDB`, `GOSUMDB`, `GOFLAGS` ou `GOTOOLCHAIN` mudarem (no ambiente do worker ou
    com `go env -w`), a decisão precisa ser revista.

## Decidir e executar

O Sentinela, e qualquer assistente que trabalhe no projeto, **nunca toma uma decisão humana**:
- cancelar ou fechar issue;
- aplicar `sentinela-fix`;
- aprovar ou fazer merge de PR;
- revogar supressão.

Quando o próximo passo é uma dessas ações, ele para e pede.

No modo local, as decisões entram pela CLI (`sentinela finding accept | reject | fix | resolve`),
e um agente só as executa a pedido explícito de quem decidiu, com `--executed-by agent --note`. Nos
modos local e MCP, as ações do Sentinela saem com a conta da pessoa (o `gh` e o MCP dela): o gate
só comenta, e o Sentinela é reconhecido no Linear pelo prefixo `**Sentinela` ([modos.md](modos.md)).

Decidir e executar são coisas diferentes, e cada desfecho guarda as duas:

| Campo | Valores | Significado |
|---|---|---|
| `outcomes.decided_by` | `human`, `agent` | quem tomou a decisão |
| `outcomes.executed_by` | `human`, `agent` | quem fez a ação no Linear ou no GitHub |

- **Decisão humana executada por agente** (a pessoa escolheu e pediu a um assistente que fizesse):
  `decided_by human`, `executed_by agent`, com uma nota. Conta normalmente nas métricas, no Curator
  e nas supressões.
- **`decided_by agent`** é uma violação. Fica fora das métricas, do Curator e das supressões, e o
  `config validate` avisa.

O Sentinela fala com o Linear como um app OAuth próprio, autorizado com `actor=app` (ADR 0032):
issues, comentários, labels e mudanças de estado saem como o app, e o sync reconhece o Sentinela
pelo autor. O token fica fora do git, em `linear_auth.token_file` (0600). Com
`fallback_api_key` ligado e o app indisponível, a chave pessoal assume e as ações voltam a sair com
o nome da pessoa. Nesse caso, os comentários do Sentinela são separados pelo prefixo `**Sentinela`.

## Dados que saem do ambiente

- Para o modelo (Anthropic, pela assinatura ou pela API): trechos do código dos repos analisados,
  sinais dos scanners e as issues do Linear relacionadas.
- Para o Linear, o Slack e o GitHub: o que vai nas issues, avisos e PRs. As evidências são trechos
  do código, com link para o GitHub.

Não use o Sentinela em código que não pode sair do ambiente sem uma política para isso.

## Retenção do que fica gravado

A limpeza roda no worker, a cada ciclo, e no `sentinela job sweep` (D-12 da revisão de prontidão):

| O quê | Onde | Quanto tempo | Config |
|---|---|---|---|
| Artefatos de cada job: `issue.md`, `spec.md`, `build.md`, reviews, `decision.md` e o `stream.jsonl` completo da sessão (com conteúdo de arquivos e saídas de comandos) | `execution.jobs` (`jobs/<id>`) | 90 dias depois do desfecho: PR mergeado ou fechado, falha ou cancelamento. Com PR aberto, ou com o achado na fila ou rodando (o retry retoma pela pasta), nunca sai | `execution.jobs_retention` (negativo: nunca) |
| Corpo dos webhooks já processados (issues, comentários e PRs do Linear e do GitHub) | `webhook_events.payload` | 30 dias depois de processado. A linha fica, com o payload vazio, para o dedup por `event_id` continuar valendo | `execution.webhook_events_retention` (negativo: nunca) |
| Worktree do job | `execution.worktrees` | Sai no ciclo seguinte ao merge ou fechamento do PR; o de falha, depois de 7 dias | `execution.failed_worktree_retention` |

**O que não expira** (defina uma política, se a segurança pedir):

- **No Postgres:** `findings` (evidências com trechos de código), `run_items` (saída de cada
  agente), `executions.artifacts` (texto de spec, build, reviews e decisão) e `llm_calls` (só
  metadados).
- **No disco:** os clones em `discovery.workdir`, os relatórios em `reports/` e as transcrições
  do Claude Code no HOME do job (`execution.home/.claude/projects`), que seguem a limpeza do
  próprio Claude Code.
