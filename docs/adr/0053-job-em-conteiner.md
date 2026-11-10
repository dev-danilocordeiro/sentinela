# 0053: Job em contêiner, com saída de rede por lista permitida

## Contexto

D-02, D-03, D-04 e D-07 da revisão de prontidão. O Claude Code do job roda Bash no worktree,
guiado por conteúdo de fora: o código do repo, os comentários da issue, as reviews e o CI. Com o
HOME próprio (ADR 0049), ele já não alcança o git, o ssh nem o gh da máquina. Mas continua sendo o
mesmo usuário do worker:

- **Segredos (D-03):** lê por caminho absoluto o `.env` (tokens do Linear, do Slack e das Apps),
  o `secrets/` (as chaves privadas das GitHub Apps), o `~/.config/sentinela` e o `~/.ssh`.
- **Docker do host (D-02):** alcança o Docker do host quando o usuário está no grupo `docker`, e
  com ele, root na máquina.
- **Rede (D-04):** tem rede livre para mandar o código para qualquer lugar.
- **Imagem (D-07):** não havia imagem pronta para rodar o job isolado.

## Decisão

- **Dois runtimes do job** (`execution.runtime`):
  - **`host`** (padrão): como hoje, com o HOME próprio;
  - **`container`:** o worker continua no host, com os segredos, e roda cada sessão do Claude Code
    num contêiner da imagem `Dockerfile.worker`, pelo docker ou pelo podman
    (`execution.container.engine`).
- **Imagem** (`Dockerfile.worker`):
  - git, Go, JDK 21, Node com corepack, os scanners atuais (golangci-lint, gosec, govulncheck,
    semgrep), o Claude Code (versão fixada), o plugin `sentinela-factory` e o binário `sentinela`
    (para o proxy de saída);
  - usuário sem privilégio `sentinela`;
  - sem `gh`.
- **O que entra no contêiner**, sempre no mesmo caminho do host (as sessões guardam o diretório de
  trabalho, e a retomada procura por ele):
  - **o worktree do job** (leitura e escrita);
  - **o diretório git comum do clone** (leitura e escrita): o `.git` de um worktree aponta para
    `<clone>/.git/worktrees/<nome>`, e o builder commita ali. O clone não guarda credencial; o
    token do push vai só no ambiente do worker (`http.extraHeader`);
  - **o diretório do job** (leitura e escrita): `job.json`, o plugin copiado e os artefatos que os
    agentes escrevem (spec, build, reviews, decisão), que são o contrato da linha;
  - **o HOME do job** (ADR 0049), montado como `/var/lib/sentinela`, com a cópia do login do Claude
    Code (0600), as sessões e os caches.

  Nada mais é montado: nem o diretório do Sentinela (`.env`, `secrets/`), nem `~/.config`, nem
  `~/.ssh`, nem o socket do Docker.
- **Usuário e privilégios:** o processo roda com o uid e o gid do worker (os arquivos montados são
  dele), `--cap-drop ALL` e `no-new-privileges`.
- **Ambiente:** só a lista fechada (`llm.ClosedEnv`), mais:
  - a identidade do git;
  - o proxy (`HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY` e, para a JVM, `JAVA_TOOL_OPTIONS`);
  - `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`.

  O `PATH` é o da imagem.
- **Rede:**
  - cada job tem uma rede própria `--internal`, sem rota para fora;
  - a única saída é um proxy (`sentinela egress-proxy`, na mesma imagem), ligado à rede do job e
    à rede padrão;
  - o proxy só aceita destinos da lista permitida (`execution.container.egress_allow`): Anthropic,
    GitHub (para clonar e baixar), proxies e registros de Go, npm, Maven, Gradle e PyPI;
  - o resto recebe 403, e o log do proxy vai para `egress.log` no diretório do job;
  - o GitHub está na lista para leitura; um push não autentica porque não há credencial no
    contêiner (o TLS do CONNECT não deixa o proxy separar leitura de escrita).
- **Ações externas continuam no worker:** push, PR, comentários e labels saem do worker, no host,
  como antes.
- **Docker para os testes do repo** (`execution.job_docker`):
  - **`none`** (padrão): sem Docker no job. Quando o `AGENTS.md` (ou `CLAUDE.md`) do repo fala em
    Docker ou Testcontainers, o job ganha um aviso, para o dono do repo saber que esses testes não
    rodam;
  - **`dind`:** um `docker:dind` privilegiado só para o job, na rede interna dele (o job o usa por
    `DOCKER_HOST`). Ele baixa imagens pelo mesmo proxy e é derrubado no fim. Os contêineres que o
    teste sobe ficam na rede interna, sem saída.
- **Limpeza:** no fim de cada sessão, o worker remove o contêiner do job, o do proxy, o do dind e a
  rede, também em timeout e cancelamento.

## Consequências

- **No runtime `container`:**
  - o job deixa de ler os segredos do Sentinela e de alcançar o Docker do host;
  - o job só fala com os destinos da lista;
  - o que ele ainda alcança é o que está montado (o worktree, o clone e o diretório do job) e o
    que a lista permite.
- **No runtime `host`:** valem os limites da ADR 0049 (mesmo usuário). Para a empresa, a
  recomendação é `container`.
- **Testes:**
  - unitários do proxy e da montagem do comando;
  - de integração, com a imagem construída (`SENTINELA_CONTAINER_TESTS=1`). De dentro do
    contêiner, falham a leitura do `.env` e do `secrets/`, o socket do Docker do host, o push, o
    `gh` e a saída para um host fora da lista; um job falso completo termina com o PR aberto pelo
    worker.
- **Custo de operação:**
  - a imagem tem alguns GB;
  - cada sessão sobe dois contêineres e uma rede (três com dind);
  - o primeiro job de cada repo baixa as dependências para o cache do HOME do job.
- **dind:** é privilegiado. Isola o Docker do host, mas não é uma fronteira contra quem controla o
  dind. Fica desligado por padrão.
