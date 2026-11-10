# testdata/ts-monorepo

Monorepo TypeScript de exemplo, no formato comum de front e BFF: `apps/web` (Next com React e
alias `@/` no tsconfig), `apps/bff` (Node com Express, que chama um backend HTTP) e
`packages/shared` (tipos e cliente HTTP), usado pelos dois pelo nome de workspace
(`@acme/shared`, `workspace:*`), em pnpm workspaces. Serve de fixture para o grafo de TypeScript,
o AGENTS.md por workspace, o eslint, o semgrep e os testes de TS no job.

A descrição fica fora do diretório do monorepo para não entrar no contexto dos agentes quando ele
é analisado.

## Bugs plantados

| Onde | Bug | Quem deveria ver |
|---|---|---|
| `apps/bff/src/routes/orders.ts`, `cancel` | `client.cancel(...)` sem `await`: o BFF responde 204 antes do backend e a rejeição fica sem tratamento | eslint `@typescript-eslint/no-floating-promises` (com tipos) ou `require-await`; Analyst |
| `apps/bff/src/backend/orders.ts`, `listOrders` | o `catch` engole o erro do backend e devolve `[]`: o resumo mostra zero pedidos com o backend fora | eslint `no-unused-vars` no `err`; Analyst |
| `apps/web/src/lib/api.ts`, `fetchSummary` | `data: any` esconde o campo errado: soma `o.amount`, e o campo de `Order` é `total` (dá `NaN`) | eslint `@typescript-eslint/no-explicit-any`; Analyst |

O `tsc --noEmit` passa nos três workspaces: os bugs não são erro de tipo.

## Para instalar (fora do repo)

O lockfile é do pnpm 9 (`packageManager`). Instale numa cópia, sem scripts, para não deixar
`node_modules` aqui:

```bash
cp -r testdata/ts-monorepo /tmp/ts-monorepo && cd /tmp/ts-monorepo
corepack enable && pnpm install --frozen-lockfile --ignore-scripts
pnpm -r run typecheck && pnpm -r run test
```
