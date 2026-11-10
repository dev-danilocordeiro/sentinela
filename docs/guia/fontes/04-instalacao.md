# Instalação passo a passo

Escolha a aba do seu modo. Os blocos com `$` mostram a saída real do binário. Nos blocos do `init`, a máquina é simulada (Docker, `gh` e `claude` presentes), e a saída é a que você vê se tudo estiver no lugar; o que fazer quando ela for diferente vem logo abaixo de cada passo.

<!-- guia:tabs instalacao -->
### Local

**Pré-requisitos**

- Docker (o Postgres com pgvector roda no `docker compose`).
- Go 1.23 ou mais novo, para compilar.
- O [Claude Code](https://docs.claude.com/en/docs/claude-code) logado numa assinatura: a descoberta e a correção usam `claude -p`.
- O [GitHub CLI](https://cli.github.com) logado (`gh auth login`), com escrita no repo que você quer analisar. Ele só é necessário para abrir PR.
- Opcional: `golangci-lint`, `gosec`, `govulncheck` e `semgrep`. Sem eles, a descoberta roda com menos sinais.

**1. Compile**

```bash
git clone <repositório do Sentinela> && cd sentinela
go build -o sentinela ./cmd/sentinela
```

Se der diferente: `go version` precisa mostrar 1.23 ou mais. Um erro de módulo pede `go mod download` com acesso à rede.

**2. Prepare**

<!-- guia:init ok init --mode local --repo sua-org/seu-repo -->

O `init` sobe o Postgres, cria o `.env` com o `DATABASE_URL` (se não existir), aplica as migrations, gera o `sentinela.yaml` mínimo e confere o `gh` e o `claude`. Rode de novo até sair `pronto.`: nada que já existe é sobrescrito.

Se der diferente, o `init` lista cada pendência numa linha `falta:`. Por exemplo, com o `gh` sem login:

<!-- guia:init gh-sem-login init --mode local --repo sua-org/seu-repo -->

| Linha `falta:` | O que fazer |
|---|---|
| `docker-compose.yml do Sentinela no diretório atual` | Rode o `init` na raiz do repo do Sentinela |
| `docker (...)` ou `subir o Postgres` | Instale o Docker e confira `docker compose ps`; a porta é `SENTINELA_PG_PORT` (padrão 5432) |
| `conexão com o banco` | O Postgres não subiu a tempo ou a porta está ocupada: `docker compose logs postgres` |
| `gh logado` | `gh auth login`, com o escopo `repo` |
| `Claude Code (claude)` | Instale e faça login no Claude Code |

**3. Descubra**

```bash
./sentinela scan --repo sua-org/seu-repo --estimate-only   # quantas chamadas a rodada faria
./sentinela scan --all
```

A primeira rodada analisa o repo inteiro (3 a 5 minutos num repo médio); as seguintes, só o que mudou. Se der diferente: um scanner `pulado` só significa que ele não está no PATH. Já uma rodada `aborted_budget` bateu no limite de chamadas (`llm.claude_code.max_calls_per_run`) ou de uso da assinatura.

**4. Leia e decida**

Abra `reports/local/index.md`, ou o `.html` de cada repo. Cada achado traz o id (`F-12`), a evidência, a correção proposta, os critérios de aceite e o comando de cada ação possível. Os comandos estão em [Dia a dia](#dia-a-dia).

**5. Corrija**

```bash
./sentinela finding fix 12
./sentinela worker --once
```

O worker cria um worktree, roda a spec, o builder, os quatro revisores e o aprovador, e abre o PR em draft pela sua conta. Se der diferente:

- **"não tem AGENTS.md nem CLAUDE.md":** o repo-alvo precisa dizer como roda os testes, o build e o lint.
- **Push com 403:** o `gh` precisa de escrita no repo.

### MCP

**Pré-requisitos:** os do modo local, mais o Linear conectado no Claude Code (claude.ai > Settings > Connectors) e, para avisos, o Slack. Confira:

```bash
claude mcp list   # deve mostrar "claude.ai Linear: ... ✔ Connected"
```

**1. Compile** como no modo local.

**2. Prepare**

<!-- guia:init ok init --mode mcp --repo sua-org/seu-repo -->

Com mais de um time no Linear, o `init` pede a chave do time:

<!-- guia:init mcp-varios-times init --mode mcp --repo sua-org/seu-repo -->

Rode de novo com `--linear-team ENG`. Sem o Linear conectado, aparece `falta: MCP do Linear conectado no Claude Code`: conecte o Linear e rode de novo.

**3. Crie as labels** no time do Linear: `sentinela`, `sentinela-fix`, `sentinela-done` e `sentinela-failed`. Depois:

```bash
./sentinela config validate
```

Saída esperada: `0 erro(s)` e os avisos do modo MCP. As ações saem com a sua conta, e o `fix_trigger` não confere quem aplicou a label. Um `label "x" não existe no time ENG do Linear` pede para criar a label.

**4. Rode**

```bash
./sentinela scan --all   # as issues saem no Linear, como você
./sentinela serve        # lê as decisões a cada adapters.mcp.poll_interval (15 min)
./sentinela worker       # roda os pedidos de correção
```

As decisões chegam com até 15 minutos de atraso. `sentinela status` mostra quantas chamadas `claude -p` o modo MCP fez por dia.

### Completo

**Pré-requisitos:** Go 1.23+, Docker (ou um Postgres 16 com a extensão `vector`), git, o Claude Code logado com uma conta da empresa e os admins de cada ferramenta. O que cada admin instala, com os escopos, está em [Uso em empresa](#uso-em-empresa).

**1. Banco, binário e config**

```bash
git clone <repositório do Sentinela> && cd sentinela
cp .env.example .env                   # segredos: nunca no git
cp sentinela.example.yaml sentinela.yaml
docker compose up -d
go build -o sentinela ./cmd/sentinela
./sentinela migrate up
./sentinela migrate status
```

Se der diferente: um `migrate up` sem conexão pede o `DATABASE_URL` certo no `.env`.

**2. Integrações** ([integracoes.md](../../integracoes.md))

- **Linear:** crie o app OAuth, ponha o client id e o secret no `.env` e rode `./sentinela linear auth`, que autoriza com `actor=app`.
- **Slack:** crie o app pelo [manifesto](../../manifests/slack-app-manifest.yaml) e convide o bot em cada canal.
- **GitHub:** crie as duas apps pelos manifestos ([bot](../../manifests/github-app-bot.json) e [revisora](../../manifests/github-app-reviewer.json)) e instale só nos repos analisados.

**3. Config.** Um arquivo só (como a [empresa pequena](#empresa-pequena-um-time-um-board-um-canal)) ou o principal mais um arquivo por time (como a [empresa com 10 times](#empresa-com-10-times-o-monorepo-acme)). Antes de chamar o Linear, confira o formato:

<!-- guia:run --config examples/empresa-pequena/sentinela.yaml config validate --offline -->

Depois, sem `--offline`, o `validate` confere o Linear, o Slack e o GitHub de verdade. As mensagens de erro, com a causa e a correção, estão em [Erros comuns](#erros-comuns).

**4. Rode sozinho**

```bash
deploy/install.sh        # instala as units do systemd (serve, worker e o timer do scan)
./sentinela status       # serviços, modo, última rodada, fila, PRs e custo de 30 dias
```

Antes de publicar, deixe `publish.enabled: false` por uma ou duas rodadas (modo sombra) e revise com `sentinela report --run <id>`.
<!-- guia:end -->
