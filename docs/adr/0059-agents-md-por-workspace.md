# 0059: AGENTS.md por workspace, com o da raiz como reserva

## Contexto

O job lia só o `AGENTS.md` (ou `CLAUDE.md`) da raiz do repo-alvo. Num monorepo de front e BFF em
TypeScript, cada workspace costuma ter os próprios comandos: o web roda `next build` e não tem
testes, e o BFF roda `vitest` e sobe com variáveis próprias. Pôr tudo num arquivo só, na raiz,
deixa o builder escolhendo entre comandos de outros workspaces. E um repo com `apps/bff/AGENTS.md`
e sem arquivo na raiz fazia todo job falhar, mesmo com os comandos do BFF documentados.

## Decisão

- **Busca:** para cada caminho do escopo do achado (`scope_paths` e, depois, os arquivos da
  evidência), o job sobe do diretório até a raiz e fica com o primeiro `AGENTS.md` (ou, no mesmo
  diretório, `CLAUDE.md`). O glob do escopo é cortado no primeiro curinga.
- **Ordem:** os arquivos encontrados entram na ordem do escopo, sem repetir, e o da raiz entra por
  último, se ainda não entrou: ele traz o setup comum do monorepo (`corepack enable`,
  `pnpm install`).
- **No job:** `job.json` leva `commands_doc` (o primeiro, o mais próximo) e `commands_docs` (todos).
  Os agentes leem os dois; em conflito, vale o `commands_doc`. A spec diz de onde vêm os comandos.
- **Falha:** o job só falha quando não há arquivo nenhum no caminho do escopo nem na raiz.
- **Avisos:** a issue publicada confere o mesmo caminho pelo clone do scan. O `status` separa os
  repos sem arquivo nenhum dos que só têm nos workspaces. O aviso de Docker (ADR 0053) confere todos
  os arquivos do achado.
- **Modelos:** `docs/agents-md-typescript.md` traz modelos para npm, pnpm e yarn, de monorepo com web
  e BFF e de repo simples. A skill `/sentinela-setup` usa esses modelos.

## Consequências

- Num achado que cruza workspaces (o web e o BFF), o principal é o do primeiro caminho do escopo,
  e o outro vai em `commands_docs`. O builder roda os dois conjuntos de testes.
- O `config validate` ainda confere só a raiz da branch padrão, pela API do GitHub. Num repo só com
  arquivos de workspace, ele avisa e diz que o do workspace serve para os achados dele. O `status`
  dá o quadro certo pelo clone.
