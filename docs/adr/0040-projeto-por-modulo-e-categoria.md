# ADR 0040: Projeto do Linear por módulo e por categoria do time; aviso com a fonte

- Status: aceita
- Data: 2026-10-11

## Contexto

Num monorepo, um time pode ter um projeto do Linear por módulo, por exemplo "Pagamentos ·
Cobrança". Também pode separar os achados de segurança num projeto próprio, como "Pagamentos ·
Segurança". Até aqui o projeto saía do time, senão do repo, senão de `defaults.linear.project`.
O `owners explain` e o `config validate` mostravam o time, mas não o canal nem a menção efetivos,
nem de onde cada um veio.

## Decisão

### Projeto do módulo

`repos[].modules[].linear_project` (ou `modules[].linear_project` em `config.d/teams/<time>.yaml`,
ADR 0041) vale quando o caminho casa com o módulo pela regra da config (passo 1 do roteamento).
Fica gravado no roteamento do achado, como o resto.

### Projeto por categoria do time

`teams.<time>.linear.category_projects` mapeia uma categoria (`bug`, `performance`,
`improvement`, `security`) para um projeto. A categoria só existe depois da análise, e o roteamento
é resolvido por caminho antes dela. Por isso esse projeto é escolhido na publicação, por
`Config.PublishProject`.

- **Ordem final:** módulo > categoria do time > o projeto gravado (time > repo > defaults) >
  `defaults.linear.project`, para achados gravados sem projeto.
- **Como a publicação reconhece o projeto de módulo:** o projeto gravado é igual ao
  `linear_project` de um módulo do mesmo time. Assim, o contrato `Finding` não muda.
- **Onde não vale:** `defaults.linear.category_projects` é recusado, porque o projeto por
  categoria só vale por time. Uma categoria inexistente também é recusada.
- **O que foi pedido e já existia:** a ordem time > repo > defaults e as labels por organização e
  por time (ADR 0038).

### Canal e menção

A menção do grupo do Slack (`teams.<time>.notify.mention`, com o ID do user group `S...`) já era
aplicada nos avisos do time. A `Resolution` agora carrega o canal e a menção efetivos e a fonte de
cada um.

### `owners explain` e `config validate`

Os dois mostram o time, o projeto, o canal e a menção efetivos e de onde vieram:

- **`owners explain`:** mostra os projetos por categoria do time.
- **`config validate`:** mostra o projeto por repo e por time, os projetos de módulo e por
  categoria, e o canal e a menção por time. Com um tracker, confere no Linear se os projetos de
  módulo e de categoria existem.

## Conferência

Antes do commit, `sentinela owners pending` na instalação real mostrou que nenhum dos 13 achados
pendentes muda de projeto: a config real não tem projeto de módulo nem por categoria.
