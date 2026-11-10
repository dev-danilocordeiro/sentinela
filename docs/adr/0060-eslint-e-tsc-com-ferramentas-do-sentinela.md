# 0060: eslint com config neutra e tsc, com as ferramentas de Node do próprio Sentinela

## Contexto

Na linha de base do ceres (2026-10-17), nenhum scanner deu sinal sobre TypeScript: o semgrep
`p/default` teve 0 sinais em 137 arquivos de Java e TS, e o resto era de Go. O eslint e o `tsc`
são as fontes óbvias, mas rodá-los do jeito do repo exige instalar as dependências dele. Isso tem
dois problemas:

- `npm ci`, `pnpm install` e `yarn` rodam scripts de `postinstall` das dependências, ou seja,
  código de terceiros, na máquina da descoberta;
- carregar o `eslint.config.js` do repo executa JavaScript do repo, com os plugins que ele pede.

A descoberta não roda código do repo (só o `go-test`, que é opcional e documentado como tal).

## Decisão

- **Ferramentas do Sentinela:** `tools/node` tem um `package.json` com versões fixas (eslint 10,
  `@eslint/js`, `typescript-eslint` 8, `eslint-plugin-react-hooks` 7 e `typescript` 5.9) e o
  lockfile. A instalação é `npm ci --ignore-scripts --prefix tools/node`. O diretório vem de
  `discovery.node_tools` (padrão `tools/node`). Sem ele, os dois scanners ficam pulados, e o motivo
  traz o comando.
- **eslint:**
  - roda com `--config tools/node/eslint.config.mjs`. É uma config neutra, que não lê nada do
    repo: o `recommended` do ESLint e do typescript-eslint, `require-await` (o `await` esquecido),
    `rules-of-hooks` e `exhaustive-deps` do React;
  - nos testes (`*.test.*`, `*.spec.*`, `__tests__/`, `test/`, `tests/`, `e2e/`), `require-await`
    e `no-explicit-any` ficam desligados: mock `async () => valor` e `any` em dublê são o normal.
    Na validação no web do ceres, 5 dos 6 sinais eram `require-await` em mocks;
  - não liga regras que dependem de tipos: sem `node_modules`, os tipos das dependências não
    resolvem. O `no-undef` fica desligado, porque sem a lista de globais do ambiente ele só daria
    ruído;
  - roda uma vez por pacote JS (`repo.BuildUnits`). O pacote da raiz ignora os workspaces, que
    rodam como unidade própria;
  - a cobertura é o número de arquivos do relatório JSON, que lista cada arquivo analisado;
  - os avisos de configuração ficam de fora: regra de um plugin que a config neutra não carrega,
    citada num `eslint-disable`, e diretiva sem uso;
  - erro vale `medium`, aviso vale `low`, e erro de parse vira `parse-error`.
- **tsc:**
  - `tsc --noEmit -p .`, do `typescript` do Sentinela, em cada pacote com `tsconfig.json`;
  - só roda com `node_modules` no pacote ou acima, até a raiz do checkout. Sem as dependências,
    todo import de pacote externo vira erro;
  - na descoberta, o clone não tem `node_modules`, então o `tsc` aparece pulado com o motivo. Onde
    as dependências estão instaladas (o worktree do job, depois do setup do AGENTS.md), ele roda;
  - não grava nada no checkout (`--incremental false`, `--composite false`), e o `--listFiles` é a
    prova de análise;
  - o diagnóstico de dentro de `node_modules` fica de fora, e o de config aponta o `tsconfig.json`.
- **Runner:** um scanner por unidade pode pular uma unidade com motivo (`unitSkipper`). Se pular
  todas, o scanner fica `skipped`, com os motivos agrupados. Se rodar alguma, os motivos vão na nota
  do `ok`.

## Consequências

- Os achados de TS ganham sinais de bug provável sem rodar nada do repo. No `testdata/ts-monorepo`,
  os três bugs plantados viram sinal (`require-await`, `no-unused-vars` no `catch` e
  `no-explicit-any`).
- As regras com tipos (`no-floating-promises`, `no-misused-promises`) ficam fora da descoberta. O
  `require-await` pega o caso comum: um handler `async` sem `await` nenhum.
- O lint do próprio repo (as regras do time) não roda na descoberta. Ele continua sendo portão no
  job, pelo AGENTS.md.
- O `tools/node` precisa ser instalado em cada instalação, com `node` no PATH. O `init` avisa.
