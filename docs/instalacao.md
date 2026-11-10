# Instalação

Para só avaliar, sem nenhuma chave, o caminho curto é o modo local: `sentinela init --mode local`
([avaliacao.md](avaliacao.md)). Esta página é a instalação completa; os três modos e o que cada
um exige estão em [modos.md](modos.md), e o guia passo a passo em
[guia-configuracao.md](guia-configuracao.md).

## Pré-requisitos

| Ferramenta | Para quê | Obrigatória |
|---|---|---|
| Go 1.27+ | compilar o `sentinela` | sim |
| Docker e Docker Compose | Postgres 16 com pgvector | sim (ou um Postgres 16 com a extensão `vector`) |
| git | clonar repos e criar worktrees | sim |
| Claude Code (`claude`) | agentes por assinatura e linha de execução (Fase 4) | sim para `llm.provider: claude_code` e para o `worker` |
| golangci-lint, gosec, govulncheck | scanners de Go | não; sem eles o scanner aparece como "pulado" |
| semgrep | scanner multi-linguagem | não; idem |
| osv-scanner (v2.6.0: `go install github.com/google/osv-scanner/v2/cmd/osv-scanner@v2.6.0`) | vulnerabilidades em lockfiles e manifestos de npm, pnpm, yarn, Maven, Gradle com lockfile e outros (ADR 0058) | não; idem |
| Node 22+ e as ferramentas de Node do Sentinela (`npm ci --ignore-scripts --prefix tools/node`) | scanners `eslint` (config neutra) e `tsc` de TypeScript e JavaScript, sem instalar nada do repo analisado (ADR 0060) | não; idem |
| GitHub CLI (`gh`) logado | PRs em draft no modo local e no MCP (`adapters.git_host: gh_cli`) | só com `gh_cli` |

Os scanners ausentes não quebram a rodada: o resumo mostra cada um como `ok` (com quanto
analisou), `pulado` (com o motivo) ou `falhou` (com a saída).

## Passo a passo

```bash
git clone <este repositório> && cd sentinela

# 1. Segredos e configuração (os dois ficam fora do git)
cp .env.example .env
cp sentinela.example.yaml sentinela.yaml

# 2. Banco
docker compose up -d                 # Postgres em localhost:5432 (porta: SENTINELA_PG_PORT)

# 3. Binário
go build -o sentinela ./cmd/sentinela
./sentinela migrate up               # aplica as migrations (todas têm down)
./sentinela migrate status
```

O binário lê o `.env` da pasta atual sem sobrescrever variáveis já definidas no ambiente. A
configuração vem de `sentinela.yaml` na pasta atual, ou de `--config <arquivo>`.

O `sentinela.example.yaml` vem no perfil de modelos qualidade, o padrão. Para orçamento ou cota
limitados, troque o bloco `models` pelo do perfil economia
([custos.md](custos.md#perfis-de-modelos)), ou gere a config com
`sentinela init --models economia`. `sentinela config models` mostra o perfil de cada papel.

## Docker

```bash
docker compose --profile app up --build   # constrói a imagem e roda `migrate up`
```

### Job em contêiner (recomendado para empresa)

O worker pode rodar cada job num contêiner isolado (`execution.runtime: container`, ADR 0053):
sem os segredos do Sentinela, sem o Docker do host e com saída de rede só para uma lista
permitida.

```bash
docker build -f Dockerfile.worker -t sentinela-job:latest .   # alguns GB; uma vez por versão
```

No `sentinela.yaml`:

```yaml
execution:
  runtime: container            # host (padrão) | container
  job_docker: none              # none | dind (Docker isolado por job, para Testcontainers)
  container:
    engine: docker              # docker | podman
    image: sentinela-job:latest
    # egress_allow: [...]       # padrão: Anthropic, GitHub, registros de Go, npm, Maven, Gradle e PyPI
```

- **Ativar:** reinicie o worker. Para voltar, troque para `runtime: host` (ou apague a chave) e
  reinicie de novo.
- **Pré-requisito:** o usuário do worker precisa usar o engine (Docker, de preferência rootless,
  ou podman).
- **O que o job enxerga:** o worktree, o clone, o diretório do job e o HOME do job. Detalhes em
  [seguranca.md](seguranca.md#runtime-do-job-host-ou-contêiner).
- **TypeScript:** a imagem tem Node 22 com corepack habilitado. O pnpm e o yarn do
  `packageManager` do repo são baixados do `registry.npmjs.org` no primeiro uso, sem pergunta
  (`COREPACK_ENABLE_DOWNLOAD_PROMPT=0`), e ficam no HOME do job. O `fetch` do Node (o do corepack)
  só usa o proxy de saída com `NODE_USE_ENV_PROXY=1`, que o job recebe; sem ele, o download falhava
  com `EAI_AGAIN`. Um `NODE_OPTIONS` próprio do repo substitui o do job
  (`--disable-warning=UNDICI-EHPA`, que só esconde o aviso do proxy). O `job.json` traz o workspace
  afetado (`workspaces`: diretório, gerenciador e pacote), e o builder roda os testes dele pelo
  AGENTS.md mais próximo (`commands_doc`), com o setup do da raiz. Conferido com o
  `testdata/ts-monorepo` (pnpm, vitest) em `TestContainerJobRunsTheTestsOfTheAffectedTypeScriptWorkspace`.

A imagem é estática (distroless, usuário não-root) e não leva `.env`, `secrets/` nem
`sentinela.yaml`: o `.dockerignore` os exclui. O `sentinela.yaml` é montado só para leitura pelo
compose. Para rodar outro comando na imagem:

```bash
docker compose --profile app run --rm app scan --repo sua-org/seu-repo --estimate-only
```

O `worker` (Fase 4) precisa do `claude` e de `git` no mesmo ambiente; na prática, rode-o no host.

## Diretórios locais

| Caminho | Conteúdo | Pode apagar? |
|---|---|---|
| `.sentinela/repos/` | clones dos repos analisados (`discovery.workdir`) | sim, são clonados de novo |
| `~/.cache/sentinela/claude-cwd/` | diretório vazio onde os agentes da descoberta rodam | sim |
| `~/.cache/sentinela/worktrees/` | um git worktree por job da linha de execução | só com nenhum job em andamento |
| `~/.cache/sentinela/jobs/` | artefatos de cada job (spec, build, reviews, decisão, stream) | sim, mas são a auditoria dos jobs |
| `~/.cache/sentinela/job-home/` | HOME do Claude Code dos jobs: login copiado, sessões e caches (`execution.home`, ADR 0049) | só com nenhum job em andamento; perde as sessões para retomar |
| `reports/` | relatórios HTML do `sentinela report` | sim |
| `secrets/` | chaves privadas das GitHub Apps | não |

Os agentes nunca rodam dentro de um diretório que tenha `CLAUDE.md` acima dele: o Claude Code
carregaria essas instruções de outro projeto. O `sentinela` recusa a configuração nesse caso.

## Atualizar

```bash
git pull
go build -o sentinela ./cmd/sentinela
./sentinela migrate up
./sentinela config validate
```

## Próximos passos

1. [Integrações](integracoes.md): Linear, Slack, GitHub Apps e Claude.
2. [Configuração](configuracao.md): repos, times e roteamento.
3. [Operação](operacao.md): rodar a descoberta, publicar e pedir correções.
