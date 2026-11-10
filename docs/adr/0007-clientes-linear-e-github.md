# ADR 0007: Clientes Linear e GitHub na Fase 1

- Status: aceita
- Data: 2026-10-09

## Decisão
- Linear: GraphQL em `https://api.linear.app/graphql`, chave pessoal crua no header `Authorization`
  (linear.app/developers/graphql). Uma query lê times com `states`, `projects` e `labels`. A página
  de referência não lista essas conexões de `Team`; confirmar com introspecção na primeira execução
  com `LINEAR_API_KEY` real.
- GitHub: REST com `X-GitHub-Api-Version: 2022-11-28`, só leitura (repos, times, usuários e o
  CODEOWNERS em `.github/`, raiz e `docs/`). Na Fase 1 autentica com `GITHUB_TOKEN` (somente
  leitura); as GitHub Apps `sentinela-bot` e `sentinela-reviewer` entram na Fase 4.
- `config validate`: erros quebram (time/projeto/estado do Linear inexistente, repo inexistente),
  avisos não (labels ausentes, times/usuários do GitHub inexistentes, owners do CODEOWNERS sem time).
  `--offline` valida só o arquivo.
- `config bootstrap`: orçamento, modelos, preços e labels vêm da config base (`--config`); times
  saem dos owners do CODEOWNERS, o dono da regra `*` vira `fallback_team`, e o estado inicial
  é `Triage` ou, se não existir, `Backlog`. Nunca sobrescreve arquivo existente.
- Testes usam servidores `httptest` no lugar das APIs (`LINEAR_API_URL`, `GITHUB_API_URL`).
