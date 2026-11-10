# AGENTS.md do BFF

BFF em Node com Express: recebe as chamadas do web e fala com o backend de pedidos por HTTP
(`BACKEND_URL`). Os tipos e o cliente vêm de `@acme/shared`.

## Comandos

Rode da raiz do monorepo, depois do setup do `AGENTS.md` da raiz (`corepack enable` e
`pnpm install --frozen-lockfile`).

- Testes (vitest): `pnpm --filter @acme/bff run test`
- Tipos: `pnpm --filter @acme/bff run typecheck`
