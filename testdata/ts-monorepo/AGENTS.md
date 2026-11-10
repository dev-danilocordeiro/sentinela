# AGENTS.md

Monorepo da loja: `apps/web` (front em React com Next), `apps/bff` (BFF em Node com Express, que
chama o backend HTTP de pedidos) e `packages/shared` (tipos e cliente HTTP usados pelos dois).
Gerenciador: pnpm workspaces, pela versão do `packageManager` (corepack).

## Setup

- `corepack enable`
- `pnpm install --frozen-lockfile`

## Comandos por workspace

Cada app tem o próprio `AGENTS.md` com os comandos dele; este vale para o que for da raiz e do
`packages/shared`.

- Tipos de tudo: `pnpm -r run typecheck`
- Testes de tudo: `pnpm -r run test`
- Shared: `pnpm --filter @acme/shared run typecheck`
