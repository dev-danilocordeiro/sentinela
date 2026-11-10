# Contribuindo

Este arquivo tem duas partes. A primeira é para os times que usam o Sentinela: como adicionar o
seu time, um módulo ou um projeto do Linear, por PR no repo onde fica a config. A segunda é para
quem mexe no código do Sentinela.

## Como adicionar seu time, módulo ou projeto

A config fica num repo com o `sentinela.yaml` principal, dos administradores, e um arquivo por
time em `config.d/teams/<time>.yaml`, de cada time (ADR 0041). O cenário completo está em
[examples/monorepo-acme](examples/monorepo-acme), e a explicação de cada regra em
[docs/guia-configuracao.md](docs/guia-configuracao.md).

### O que você pode mudar sozinho (no arquivo do seu time)

- **Board e projeto no Linear:** `linear.team`, `linear.project`, `linear.category_projects` (ex.:
  segurança num projeto próprio) e os estados.
- **Canal e menção:** `notify.slack` (ID `C...`) e `notify.mention` (ID do grupo `S...`; um
  `@grupo` não notifica).
- **Seus módulos nos repos compartilhados:** `modules`, com `repo`, `path` e `linear_project`.
- **Seus repos inteiros:** `repos`.
- **Suas labels:** `labels.trigger`, `labels.done` e `labels.failed`.
- **Políticas que só apertam:** `policy.blocked`, `policy.max_lines`, `policy.max_files` e
  `blocked_paths` a mais.

O que é da organização (auto-aprovação, revisora que aprova, modelos, custos, modos) fica no
principal: peça aos administradores. O `config validate` recusa essas chaves no seu arquivo.

### PR de exemplo: o time catálogo ganha um módulo com projeto próprio

O catálogo quer que os achados de `modules/busca` no monorepo vão para um projeto novo, "Catálogo
· Busca".

1. **Crie o projeto no Linear**, no time CAT, com esse nome exato.
2. **Edite só o seu arquivo**, `config.d/teams/catalogo.yaml`:

   ```yaml
   modules:
     - { repo: acme/plataforma, path: "modules/busca/**", linear_project: "Catálogo · Busca" }
   ```

3. **Confira localmente**, antes de abrir o PR:

   ```bash
   sentinela config validate --offline --changed config.d/teams/catalogo.yaml --base origin/main \
     --codeowners acme/plataforma=caminho/do/CODEOWNERS
   ```

   A saída mostra, para cada caminho afetado, o roteamento antes e depois:

   ```
   acme/plataforma modules/busca/exemplo:
       antes:  time catalogo [codeowners:L17] · projeto Catálogo [teams.catalogo.linear.project (config.d/teams/catalogo.yaml)] · ...
       depois: time catalogo [config:modules/busca/**] · projeto Catálogo · Busca [repos[acme/plataforma].modules[modules/busca/**].linear_project (config.d/teams/catalogo.yaml)] · ...
   ```

4. **Abra o PR.** O workflow do repo roda o mesmo comando e comenta o resultado no PR. O
   CODEOWNERS do repo da config pede a aprovação do seu time (dono do arquivo).
5. **Depois do merge,** `serve`, `worker` e `scan` leem a config nova no ciclo seguinte, sem
   reiniciar. Os achados já publicados ficam onde estão; os novos vão para o projeto novo.

Um time novo é o mesmo PR, com um arquivo novo: `config.d/teams/<time>.yaml` com `github`,
`linear`, `notify` e os módulos ou repos. Inclua também no CODEOWNERS do repo da config a linha
que dá o arquivo ao time; essa parte é dos administradores.

### O que o revisor deve olhar

- **O comentário do workflow:** o "antes e depois" de cada caminho afetado é o que muda de verdade.
  Desconfie de caminhos que mudam de time sem o PR dizer por quê.
- **Globs:** o mais longo vence. Um glob curto e largo (`modules/**`) pode tirar caminhos de outros
  times; um módulo igual ao de outro time é recusado.
- **Projetos e labels:** precisam existir no Linear antes do merge, senão o `config validate`
  completo (sem `--offline`) falha e a publicação do time para.
- **IDs do Slack:** canal `C...` e grupo `S...`. Um nome (`#canal`) funciona, mas é resolvido a
  cada início; um handle (`@grupo`) não notifica.
- **Política:** o arquivo de um time só aperta. Pedidos para liberar auto-aprovação vão para o
  principal.

### Erros comuns

| O `config validate` diz | O que fazer |
|---|---|
| `chave "models" só vale no sentinela.yaml principal: ...` | Tire a chave do seu arquivo; peça aos administradores |
| `chave "policy.auto_approve" só vale no sentinela.yaml principal: ...` | Idem: auto-aprovação é da organização |
| `o módulo "modules/x/**" está em dois times: ...` | Combine com o outro time quem é o dono; um dos dois tira a linha |
| `modules: o repo acme/x não está no sentinela.yaml ...` | O repo compartilhado precisa entrar no principal; se ele for só seu, declare-o em `repos` |
| `field canal not found in type config.TeamFile` | Chave errada: use `schema/team.schema.json` no editor |
| `categoria "seguranca" não existe (use bug, performance, improvement, security)` | Use o nome em inglês |
| `label "x" não existe no time PAG do Linear` | Crie a label no time do Linear |

A lista completa, com as causas, está em
[docs/guia-configuracao.md](docs/guia-configuracao.md#erros-comuns).

## Mexendo no código do Sentinela

- **Leitura obrigatória:** o [CLAUDE.md](CLAUDE.md), com os princípios e o contrato `Finding`, e
  as ADRs em [docs/adr](docs/adr). Uma decisão relevante nova vira uma ADR curta.
- **Testes e lint:**

  ```bash
  createdb sentinela_test   # uma vez; os testes de integração apagam o esquema do banco que recebem
  SENTINELA_TEST_DATABASE_URL=postgres://.../sentinela_test?sslmode=disable go test -p 1 ./...
  golangci-lint run
  ```

  Nunca aponte `SENTINELA_TEST_DATABASE_URL` para o banco real.
- **CI:** o `.github/workflows/ci.yml` roda o mesmo em cada push e PR: `go test -p 1 ./...` num
  Postgres com pgvector de serviço (`sentinela_test`), o `golangci-lint` na versão fixada e, se o
  repo tiver o `scripts/exportar.sh`, uma exportação com a checagem de termos proibidos.
- **Mudou uma chave de config:** regenere o schema
  (`go run ./cmd/sentinela config schema > schema/sentinela.schema.json` e `--team` para
  `schema/team.schema.json`) e atualize `docs/configuracao.md`. Os testes
  `TestSchemaFilesAreUpToDate`, `TestDocsConfigKeysExist` e `TestDocsCommandsAndFlagsExist` falham
  se o schema ou a documentação ficarem para trás.
- **Mudou o roteamento:** o `TestAcmeExampleValidates` confere o cenário de referência; atualize
  o exemplo e o guia juntos.
- **Mudou um doc, um exemplo, o schema, um comando ou o roteamento:** regenere o guia com
  `go run ./cmd/sentinela docs guide` e commite `docs/guia/index.html`. O
  `TestGuideIsUpToDate` falha enquanto ele estiver diferente do gerado. O texto próprio do guia
  fica em `docs/guia/fontes/`.
- **Migrations:** toda migration tem `down`.
