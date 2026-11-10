# Times

O Sentinela decide o time dono de cada achado pelo caminho do arquivo: módulos da config, depois
o CODEOWNERS, depois o time do repo e o fallback (a ordem está em `docs/guia-configuracao.md`).
Aqui você propõe os times, grava a config e mostra o resultado em caminhos reais.

## 1. Propor os times

- **Modo completo:** gere uma proposta a partir do CODEOWNERS e dos times do Linear:

  ```bash
  ./sentinela config bootstrap --repo acme/svc --models qualidade -o sentinela.bootstrap.yaml
  ```

  Ela precisa do Linear e do GitHub configurados no `.env`. Leia a proposta com a pessoa e copie
  os times aceitos.
- **Modos local e MCP:** leia o CODEOWNERS do clone (`.github/CODEOWNERS`, `CODEOWNERS` ou
  `docs/CODEOWNERS`) e liste os donos (`@org/time` ou `@pessoa`) com os caminhos de cada um. Sem
  CODEOWNERS, proponha um time só para o repo.

## 2. Perguntar, time por time

Uma pergunta por vez, com a sugestão marcada:

| Modo | O que perguntar |
|---|---|
| local | o nome do time (minúsculas, sem espaço) e quais donos do CODEOWNERS são dele |
| MCP | o nome; a chave do time no Linear (ex.: ENG) e o projeto; o canal do Slack (ID `C...`) e o grupo a mencionar (ID `S...`), se houver |
| completo | o mesmo do MCP; o responsável (`assignee`: none, lead ou round_robin) e a política (`auto_approve`, `blocked` para domínio sensível) |

IDs do Slack: o ID do canal aparece no Slack em "Ver detalhes do canal"; o de um grupo, no perfil
do grupo. Um `@grupo` escrito como texto não notifica ninguém.

## 3. Gravar a config

- **Local e MCP:** os times vão no `sentinela.yaml`, em `teams`, e cada repo ganha `team` (ou
  `modules` com `path` e `team`, num monorepo):

  ```yaml
  teams:
    plataforma:
      github: ["@acme/plataforma"]
      linear: { team: ENG, project: "Sentinela · Plataforma" }   # só no MCP
  repos:
    - { name: acme/svc, team: plataforma }
  ```

- **Completo:** um arquivo por time em `config.d/teams/<time>.yaml` (veja o `CONTRIBUTING.md` e
  `examples/monorepo-acme`), e o principal só com o que é da organização.

Mostre o diff antes de gravar e grave só com o OK.

## 4. Conferir com caminhos reais

Para cada repo, escolha de 3 a 5 caminhos reais do clone, de donos diferentes e um sem dono (um
arquivo da raiz, como o `Makefile` ou o `docker-compose.yml`), e mostre:

```bash
./sentinela owners explain acme/svc cmd/api/main.go --codeowners "$dir/svc/.github/CODEOWNERS"
```

O `--codeowners` usa o CODEOWNERS do clone, sem buscar no GitHub. Pergunte se cada caminho foi
para o time certo; se não, ajuste `modules` (o glob mais longo vence) e rode de novo.

**Caminho sem dono.** Num monorepo com `modules` só para os subdiretórios (`api/**`, `web/**`) e
sem CODEOWNERS, os arquivos da raiz (CI, compose, `Makefile`, docs) caem em `defaults`, sem time:
o `owners explain` mostra `time: (nenhum; defaults)`. Pergunte de quem eles são e proponha uma
destas saídas:

- `fallback_team: <time>` no repo (o mais simples: tudo que nada cobre vai para esse time);
- um módulo para eles (`{ path: ".github/**", team: plataforma }`, ou `override: true` para
  arquivos transversais como `**/Dockerfile`).

Termine com `./sentinela config validate --offline`. Ele avisa quando ainda sobram arquivos em
`defaults` sem time; com o clone do scan, mostra quantos e exemplos.
