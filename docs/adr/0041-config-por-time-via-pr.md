# ADR 0041: Configuração mantida pelos times via PR (config.d/teams), validate offline, schema e releitura

- Status: aceita
- Data: 2026-10-11

## Contexto

Com vários times, um `sentinela.yaml` único vira gargalo. Toda mudança de módulo, projeto, canal
ou label de um time passa pela mesma pessoa e pelo mesmo arquivo, e é fácil um time mexer, sem
querer, na política de outro ou da organização.

## Decisão

### Um arquivo por time

`config.d/teams/<time>.yaml`, ao lado do `sentinela.yaml`, é juntado ao principal no `Load`. O
nome do arquivo é o nome do time, e o time não pode estar também em `teams` do principal.

| Chave | O que é |
|---|---|
| `github`, `members` | Como o time aparece no CODEOWNERS |
| `linear` | Time, projeto, estados, labels e `category_projects` |
| `assignee`, `lead` | Responsável pelas issues |
| `notify` | Canal e menção do grupo do Slack |
| `labels` | Gatilho, done e failed |
| `policy` | Só `blocked`, `max_lines` e `max_files` |
| `modules` | `repo`, `path` e `linear_project`, em repos declarados no principal |
| `repos` | Repos inteiros do time, com `name`, `default_branch`, `languages` e `linear_project` |
| `blocked_paths` | Caminhos bloqueados; só acrescenta à lista da organização |

**Política da organização só vale no principal:** auto-aprovação (`policy.auto_approve`),
`reviewer_approves`, modelos, limites de custo, preços, adapters, execução, descoberta, padrões e
outros times. A chave num arquivo de time é recusada com a chave, o arquivo e o porquê, por
exemplo:

```
config.d/teams/pagamentos.yaml: chave "models" só vale no sentinela.yaml principal: modelos e custos são da organização
```

Chaves desconhecidas são recusadas pelo decode estrito.

**Conflitos:** o mesmo caminho de módulo em dois times no mesmo repo é erro, no principal, nos
arquivos de time ou entre eles. Um repo inteiro declarado por um time não pode existir em outro
lugar.

**Sem migração obrigatória:** sem `config.d`, nada muda, e a instalação real segue com o
`sentinela.yaml` único.

### `config validate --offline`

Valida estrutura, política e roteamento sem chamar Linear, Slack nem GitHub. Mostra os times de
cada arquivo e o projeto, o canal e a menção efetivos, com a fonte.

`--changed <arquivos> [--base origin/main] [--codeowners owner/repo=arquivo]` mostra o
`owners explain` dos caminhos que os arquivos mudados afetam, antes (a config no commit base,
lida do git) e depois (os arquivos atuais):

- os módulos e os repos dos times mudados;
- todos os caminhos, se o principal mudou.

O workflow do PR usa isso para comentar o efeito da mudança (`examples/monorepo-acme`).

### JSON Schema

`schema/sentinela.schema.json` e `schema/team.schema.json` são gerados das structs do pacote
`config` (`sentinela config schema [--team]`). O teste `TestSchemaFilesAreUpToDate` falha se o
arquivo ficar diferente do código.

### Releitura sem reiniciar

`serve`, `worker` e `scan --all` releem a config a cada ciclo (antes de cada repo, no scan).

- **Config inválida:** fica a última válida, e o aviso vai para o log e uma vez para o canal padrão
  (`defaults.notify.slack`) por erro diferente.
- **O que só muda no reinício:** adapters, banco, credenciais e o intervalo do ciclo. Os clientes
  já montados continuam os mesmos.

## Consequências

- Cada time abre PR só no seu arquivo, e o CODEOWNERS do repo do Sentinela pode dar a cada time o
  seu arquivo e deixar o principal com os administradores.
- O `owners explain`, o `config validate` e o `--changed` dizem de que arquivo veio cada decisão.
- Um módulo de time só pode ficar em repo declarado no principal, para os repos compartilhados
  continuarem sob a organização.
