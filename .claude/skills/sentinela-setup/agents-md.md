# Estrutura do repo e AGENTS.md

O job de correção lê os comandos de setup, build, teste e lint do `AGENTS.md` (ou `CLAUDE.md`) do
repo-alvo: o mais próximo do escopo do achado (num monorepo, o do workspace) e o da raiz como
reserva (ADR 0059). Sem nenhum no caminho, o job falha com uma mensagem dizendo o que falta. Aqui você descobre a
estrutura do repo e, se o arquivo faltar, escreve um rascunho para a pessoa aprovar.

## 1. Clonar para ler

```bash
dir=$(mktemp -d -t sentinela-setup-XXXXXX)
git clone --depth 1 https://github.com/acme/svc.git "$dir/svc"
```

Use o `gh repo clone acme/svc "$dir/svc" -- --depth 1` se o repo for privado. Não rode nada do
repo ainda. Use sempre o prefixo `sentinela-setup-` no `mktemp`, para a pasta ser reconhecível, e
apague no fim só ela: `rm -rf "$dir"`.

## 2. Detectar linguagens e estrutura

Procure, a partir da raiz (sem entrar em `node_modules`, `vendor`, `build`, `dist`, `.git`):

| Sinal | O que significa | Unidade |
|---|---|---|
| `go.mod` (um ou vários) | módulos Go | cada diretório com `go.mod` |
| `go.work` | workspace Go | os módulos listados nele |
| `settings.gradle.kts` ou `settings.gradle` com `include(...)` | build Gradle multimódulo | cada módulo incluído |
| `build.gradle.kts` ou `build.gradle` sem settings com `include` | Gradle de um módulo | a raiz |
| `gradlew` | wrapper do Gradle (use sempre ele) | — |
| `pom.xml` com `<modules>` | Maven multimódulo | cada módulo |
| `package.json` com `workspaces` | workspaces npm ou yarn | cada pacote dos globs |
| `pnpm-workspace.yaml` | workspaces pnpm | cada pacote dos globs |
| `package-lock.json`, `yarn.lock`, `pnpm-lock.yaml` | gerenciador de pacotes | — |
| campo `packageManager` do `package.json` | versão do npm, pnpm ou yarn (corepack) | — |
| `tsconfig.json` | TypeScript | — |
| `.github/workflows/*.yml` | os comandos que o CI roda de verdade | — |
| `Makefile`, `justfile`, `Taskfile.yml` | comandos do projeto | — |
| `docker-compose.yml`, menção a Testcontainers | os testes precisam de Docker | — |

Mostre à pessoa um resumo: linguagens, unidades (módulos ou pacotes), gerenciador e se os testes
precisam de Docker. Se os testes precisarem de Docker e a empresa for usar o runtime de contêiner,
avise que é preciso `execution.job_docker: dind` (veja `docs/instalacao.md`, Job em contêiner).

### Scanners por linguagem

O `init` e o `config bootstrap` geram `discovery.scanners` por esta tabela, pelas linguagens dos
repos (com o comentário de onde elas vieram). Confira a linha gerada contra o que você detectou:

| Linguagem | Scanners que se aplicam | O que fica sem cobertura hoje |
|---|---|---|
| Go | `golangci-lint`, `gosec`, `govulncheck`, `semgrep` (e `go-test`, que executa código do repo e só entra à mão) | — |
| Java, Kotlin (Maven, Gradle) | `semgrep`, `osv-scanner` (Gradle só com `gradle.lockfile`) | análise própria da linguagem (SpotBugs, PMD) |
| TypeScript, JavaScript | `semgrep` (com `p/typescript`, `p/react`, `p/javascript`, `p/nodejs` e `p/expressjs`), `eslint` (config neutra), `tsc` (pulado na descoberta, sem `node_modules`) e `osv-scanner` (lockfiles) | as regras do lint do próprio time (rodam no job, pelo AGENTS.md) e as regras do eslint que dependem de tipos |
| Python e outras | `semgrep` | análise própria e dependências |

- Com TypeScript ou JavaScript, instale as ferramentas de Node do Sentinela:
  `npm ci --ignore-scripts --prefix tools/node`. É o eslint com a config neutra e o `tsc`, nas
  versões do lockfile do Sentinela; não roda nada do repo (ADR 0060).
- Scanner que não se aplica aparece no resumo do scan como pulado ("não se aplica às linguagens do
  repo"). Proponha tirá-lo da lista para o resumo ficar limpo.
- Diga à pessoa o que fica sem cobertura. Os achados nessas linguagens vêm só dos modelos, e
  dependência vulnerável não é detectada.
- Se ela quiser uma linha de base de dependências, `npm audit --omit=dev` (num clone com
  `package-lock.json`) mostra o que o Sentinela ainda não pega. Ele não roda código do repo, mas
  manda a lista de dependências ao registro do npm: pergunte antes.

## 3. AGENTS.md ou CLAUDE.md

```bash
find "$dir/svc" \( -name node_modules -o -name .git \) -prune -o \( -name AGENTS.md -o -name CLAUDE.md \) -print
```

O job usa o mais próximo do achado: num achado em `apps/bff/src`, vale o `apps/bff/AGENTS.md`, e
o da raiz entra como reserva, com o setup comum. Sem arquivo no workspace, vale o da raiz. Num
monorepo de front e BFF, proponha um por workspace (só os comandos dele) e o setup na raiz.

Se existir, leia e confira que diz como instalar dependências, compilar, testar e rodar o lint.
Depois, **confira que cada comando listado funciona** (com o OK da pessoa: eles executam código do
repo):

1. num clone limpo e temporário (`mktemp -d -t sentinela-setup-XXXXXX`), rode cada comando de
   setup, build, teste e lint que o arquivo cita, um por vez, guardando o código de saída e as
   últimas linhas;
2. se um comando sobe a infra do repo (`docker compose`, `make up`), siga a seção 5 antes;
3. mostre uma tabela: comando, passou ou falhou, e o motivo da falha (ex.: "`make lint`: checkstyle
   sem config no pom, 339 violações"; "`./mvnw verify`: Testcontainers não acha o Docker 29");
4. um comando que falha faz toda correção falhar no portão. Proponha um PR em draft que atualize
   a seção de comandos para os que passam e diga o que fica fora até ser corrigido. É o mesmo fluxo
   do rascunho abaixo; o merge é da pessoa.

## 4. Rascunho, quando faltar

Tire os comandos do próprio repo, nesta ordem de confiança: o CI (`.github/workflows`), o
`Makefile` (ou equivalente), os `scripts` do `package.json`, o README. Não invente comando.

Para conferir que um comando funciona, **pergunte antes** de rodar qualquer coisa que execute
código do repo. Isso inclui `go test`, `./gradlew`, `npm ci` e `npm install` (que rodam scripts de
`postinstall`), `pnpm install`, `yarn` e `make`. Sem o OK, deixe o comando marcado como "não
conferido".

Modelos de partida (ajuste aos comandos reais do repo):

**Go:**

```markdown
# AGENTS.md

## Setup
- Go 1.xx (linha `go` do go.mod).

## Build e testes
- `go build ./...`
- `go test ./...`

## Lint
- `golangci-lint run` (config em .golangci.yml)
```

**Kotlin ou Java com Gradle:**

```markdown
# AGENTS.md

## Setup
- JDK 21. Use sempre o wrapper (`./gradlew`), nunca um Gradle do sistema.

## Build e testes
- `./gradlew build`
- `./gradlew test` (os de integração precisam de Docker, se usarem Testcontainers)

## Lint
- `./gradlew check` (ou o plugin do repo: detekt, ktlint, spotless)
```

**Java ou Kotlin com Maven (inclusive multimódulo):**

```markdown
# AGENTS.md

## Setup
- JDK 21 (ou o do `.mise.toml`, `.tool-versions` ou `.sdkmanrc`). Use sempre o wrapper
  (`./mvnw`), nunca um Maven do sistema. Num monorepo: `./api/mvnw -f api/pom.xml`.

## Build e testes
- `./mvnw -q -DskipTests package` (build sem testes)
- `./mvnw test` (unidade: `*Test`, sem Docker)
- `./mvnw verify` (integração: `*IT` pelo failsafe; precisa de Docker se usar Testcontainers)
- Um módulo só: `./mvnw -pl <módulo> -am test`

## Lint
- O plugin que o `pom.xml` configura (checkstyle, spotless, PMD). Sem configuração no pom,
  `checkstyle:check` cai nas regras padrão e reprova quase tudo: não use como portão.
```

**TypeScript com npm, pnpm ou yarn:** use os modelos de
[docs/agents-md-typescript.md](../../../docs/agents-md-typescript.md), que cobrem monorepo com web
e BFF (um AGENTS.md na raiz com o setup e um por workspace) e repo simples, nos três
gerenciadores. Escolha pelo lockfile (`pnpm-lock.yaml`, `yarn.lock` e `.yarnrc.yml` para o yarn
moderno, `package-lock.json`) e pelo `packageManager`. Confira nos scripts do `package.json` se os
testes são jest ou vitest e use a forma sem watch (`vitest run`, `jest --ci`).

Mostre o rascunho inteiro e pergunte: "abro um PR em draft com este AGENTS.md em acme/svc?". Só com
um sim explícito:

```bash
cd "$dir/svc"
git fetch --unshallow
git switch -c sentinela/agents-md
git add AGENTS.md
git commit -m "AGENTS.md: comandos de setup, build, teste e lint"
gh pr create --draft --title "AGENTS.md para o Sentinela" --body "Comandos que o Sentinela usa na correção (docs/avaliacao.md)."
```

Mostre o link do PR. O merge é da pessoa. Até o merge, a correção desse repo não roda (o
`./sentinela status` e o `./sentinela config validate` avisam).

## 5. Rodar os comandos do repo sem colidir com a máquina

Conferir os testes de integração costuma subir a infra do próprio repo (`docker compose up`,
`make up`, Testcontainers). Antes, olhe o `docker-compose.yml` dele e a máquina:

```bash
grep -nE '^name:|container_name:|ports:' "$dir/svc"/docker-compose.y*ml
docker ps -a --format '{{.Names}} {{.Status}}'
docker volume ls
```

- **Nome fixo:** com `container_name:` ou `name:` no topo, e contêineres ou volumes com esses nomes
  já na máquina (o ambiente de desenvolvimento da pessoa), rodar o compose do repo colide com
  eles, ou pior, reaproveita o volume e o banco de desenvolvimento dela. Não rode assim.
- **Rode num projeto próprio, com nomes, portas e label próprios.** Um arquivo de override (fora
  do clone) troca só o `container_name` e põe o label:

  ```yaml
  # override.yml: só para conferir os comandos; não toca nos contêineres da máquina
  services:
    db:
      container_name: sentinela-setup-svc-db
      labels: { sentinela.setup: svc-check }
  volumes:
    pgdata:
      labels: { sentinela.setup: svc-check }
  ```

  ```bash
  docker compose -f docker-compose.yml -f "$override" -p sentinela-setup-svc-check up -d --wait
  # num Makefile que chama o compose por variável:
  make COMPOSE="docker compose -f docker-compose.yml -f $override -p sentinela-setup-svc-check" \
    POSTGRES_HOST_PORT=55434 test-it
  ```

  Troque as portas por variáveis de ambiente do próprio compose (as do `.env.example`) para
  portas livres em `ss -ltn`.
- **No fim, apague só o que você criou:** `docker compose -p sentinela-setup-svc-check down -v`,
  e confira pelo label (`docker ps -a --filter label=sentinela.setup=svc-check`). Nunca use
  `docker system prune` nem `docker volume prune`. Contêineres do Testcontainers saem sozinhos (o
  Ryuk remove).
