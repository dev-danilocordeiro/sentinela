# Modelos de AGENTS.md para TypeScript

O job de correção tira os comandos de setup, build, teste e lint do `AGENTS.md` (ou `CLAUDE.md`)
do repo-alvo. Os agentes não carregam nada específico de repo. Este arquivo traz modelos para front
e BFF em TypeScript com npm, pnpm e yarn, de monorepo e de repo simples. A skill `/sentinela-setup`
parte deles; ajuste sempre aos comandos reais do repo (CI, scripts do `package.json`, README).

## Como o job escolhe o arquivo (ADR 0059)

- Para cada caminho do escopo do achado, o job sobe até a raiz e fica com o primeiro `AGENTS.md`.
  Num achado em `apps/bff/src/routes`, vale o `apps/bff/AGENTS.md`.
- O da raiz entra sempre como reserva, por último: ponha nele o setup comum (Node, corepack,
  instalação) e o que vale para todos os workspaces.
- No arquivo do workspace, ponha só o que é dele: testes, tipos, lint e build daquele pacote.
- Sem arquivo no workspace, vale o da raiz. Sem nenhum no caminho, o job falha dizendo o que falta.

## O que todo modelo precisa ter

- **Versão do Node e do gerenciador:** o job em contêiner (ADR 0053) tem Node 22 com corepack. O
  `packageManager` do `package.json` (`pnpm@9.15.9`, `yarn@4.9.2`) decide a versão. Sem ele, diga
  qual usar.
- **Instalação reproduzível:** `npm ci`, `pnpm install --frozen-lockfile` ou
  `yarn install --immutable`. Nunca `npm install`: ele reescreve o lockfile, e o diff do PR passa
  a ter mudanças que ninguém pediu.
- **Testes sem modo watch:** `vitest run` (não `vitest`) e `jest --ci`. Um teste em watch nunca
  termina, e o job para no timeout de inatividade.
- **Tipos:** `tsc --noEmit -p <workspace>`. Num Next, o `next build` também checa, mas é mais lento.
- **Lint:** o comando do repo (`eslint .`). Se o lint do repo não passa hoje, diga isso no arquivo
  em vez de listá-lo como portão: um comando vermelho reprova toda correção.
- **O que precisa de rede ou de Docker:** testes que sobem banco ou chamam o backend de verdade.
  Diga como pular ou como subir, e se o job em contêiner precisa de `execution.job_docker: dind`.

## Monorepo com pnpm workspaces (web e BFF)

Estrutura: `apps/web` (Next), `apps/bff` (Express ou Nest), `packages/shared` (tipos e cliente),
`pnpm-workspace.yaml` na raiz. É o formato de `testdata/ts-monorepo`.

`AGENTS.md` (raiz):

```markdown
# AGENTS.md

Monorepo: `apps/web` (Next), `apps/bff` (BFF em Node, chama o backend por HTTP) e
`packages/shared` (tipos e cliente HTTP dos dois). pnpm workspaces.

## Setup
- Node 22. `corepack enable` (o pnpm vem do `packageManager` do package.json).
- `pnpm install --frozen-lockfile`

## Comandos
- Cada app tem o próprio AGENTS.md; use o do workspace que você mudou.
- Mudou `packages/shared`: rode os testes e os tipos de todos (`pnpm -r run typecheck`,
  `pnpm -r run test`), porque web e BFF dependem dele.
- Tipos do shared: `pnpm --filter @acme/shared run typecheck`
```

`apps/web/AGENTS.md`:

```markdown
# AGENTS.md do web

Next com React. Alias `@/` para `src/` (tsconfig). Rode da raiz, depois do setup do AGENTS.md
da raiz.

- Tipos: `pnpm --filter @acme/web run typecheck`
- Lint: `pnpm --filter @acme/web run lint`
- Testes: `pnpm --filter @acme/web run test` (vitest, sem watch)
- Build: `pnpm --filter @acme/web run build` (lento; rode no fim)
```

`apps/bff/AGENTS.md`:

```markdown
# AGENTS.md do BFF

Node com Express (ou Nest). Fala com o backend por HTTP (`BACKEND_URL`); os testes usam um
backend falso, sem rede. Rode da raiz, depois do setup do AGENTS.md da raiz.

- Testes: `pnpm --filter @acme/bff run test` (vitest run, ou jest --ci)
- Tipos: `pnpm --filter @acme/bff run typecheck`
- Lint: `pnpm --filter @acme/bff run lint`
```

## Monorepo com npm workspaces

Mesma estrutura, com `"workspaces": ["apps/*", "packages/*"]` no `package.json` da raiz e
`package-lock.json`. Muda só o setup e o jeito de escolher o workspace:

```markdown
## Setup
- Node 22 (o npm que vem com ele).
- `npm ci`

## Comandos (no AGENTS.md de cada app)
- Testes: `npm run test -w @acme/bff`
- Tipos: `npm run typecheck -w @acme/bff`
- Todos os workspaces: `npm run test --workspaces --if-present`
```

## Monorepo com yarn workspaces

Com yarn moderno (Berry, `yarn@4` no `packageManager`, `.yarnrc.yml`):

```markdown
## Setup
- Node 22. `corepack enable` (o yarn vem do `packageManager`).
- `yarn install --immutable`

## Comandos (no AGENTS.md de cada app)
- Testes: `yarn workspace @acme/bff run test`
- Tipos: `yarn workspace @acme/bff run typecheck`
- Todos os workspaces: `yarn workspaces foreach -A run test`
```

Com yarn clássico (1.x, sem `.yarnrc.yml`): `yarn install --frozen-lockfile`,
`yarn workspace @acme/bff test` e, para todos, `yarn workspaces run test`.

## Repo simples: só o web

Um repo, um `package.json`, sem workspaces. Um AGENTS.md só, na raiz:

```markdown
# AGENTS.md

Front em Next com React e TypeScript. Alias `@/` para `src/`.

## Setup
- Node 22. `npm ci` (pnpm: `corepack enable && pnpm install --frozen-lockfile`;
  yarn: `corepack enable && yarn install --immutable`)

## Comandos
- Tipos: `npx tsc --noEmit`
- Lint: `npm run lint`
- Testes: `npm test -- --run` (vitest) ou `npm test -- --ci` (jest). Sem testes no repo: diga
  isso aqui, para o builder não procurar.
- Build: `npm run build`
```

## Repo simples: só o BFF

```markdown
# AGENTS.md

BFF em Node com Express (ou Nest) e TypeScript. Chama o backend por HTTP (`BACKEND_URL`).

## Setup
- Node 22. `npm ci` (ou o equivalente do pnpm ou do yarn, como no modelo do web).

## Comandos
- Testes: `npm test -- --run` (vitest) ou `npm test -- --ci` (jest). Os testes não chamam o
  backend de verdade: usam um servidor falso (msw, nock ou supertest com o cliente trocado).
- Tipos: `npx tsc --noEmit`
- Lint: `npm run lint`
- Build: `npm run build` (Nest: `nest build`)
```

## Como conferir um modelo

Rode cada comando num clone limpo e temporário, um por vez, e guarde o código de saída (a skill
`/sentinela-setup` faz isso com o seu OK, porque a instalação roda scripts do repo). Um comando
que falha hoje sai da lista de portões, com uma linha dizendo por quê.
