# Guia de configuração

Este guia vai do zero até uma organização com vários times configurada no Sentinela: o que é cada
coisa, como instalar em cada modo, como o Sentinela decide quem é dono de cada achado e para onde
ele vai, e como os times mantêm a própria configuração por PR. Ele monta o cenário de referência
`examples/monorepo-acme` passo a passo, com a saída real dos comandos.

O [guia interativo](guia/index.html) (um HTML que abre direto do disco) tem tudo isto com
simulador de roteamento, gerador de config e busca. Para uso numa empresa (provider, onde rodar,
identidade, permissões, adoção), veja [empresa.md](empresa.md). Referências curtas: os três modos em [modos.md](modos.md), cada chave em
[configuracao.md](configuracao.md), e como um time abre o PR do seu arquivo em
[CONTRIBUTING.md](../CONTRIBUTING.md).

## Glossário

| Termo | O que é |
|---|---|
| **Achado** | Um problema que o Sentinela encontrou, com evidência `arquivo:linha`, categoria (`bug`, `performance`, `improvement`, `security`), severidade, correção proposta e critérios de aceite. Vira issue no Linear (ou entrada no relatório, no modo local). Cada um tem um fingerprint, para não ser publicado duas vezes |
| **Unidade** | O pedaço de código que a descoberta examina de cada vez: um pacote (um diretório de código). O Triager faz uma chamada por unidade e decide se vale aprofundar |
| **Time** | Um grupo de pessoas dono de código: tem um board no Linear, um canal no Slack, um grupo do Slack para menção e uma política. É declarado em `teams` ou em `config.d/teams/<time>.yaml` |
| **Módulo** | Um caminho (glob) de um repo com dono explícito na config, como `modules/cobranca/**`. Pode ter projeto próprio no Linear. Sem módulo, o dono vem do CODEOWNERS |
| **Projeto** | O projeto do Linear em que a issue é criada, dentro do board do time |
| **Label de gatilho** | A label que uma pessoa aplica na issue para pedir a correção (padrão do exemplo: `sentinela-fix`; cada time pode ter a sua). O Sentinela nunca a aplica |
| **Supressão** | O registro de uma recusa: o achado (pelo fingerprint) e o motivo. Ele não volta, e o motivo ensina o Judge |
| **Gate** | O Policy Gate: código que decide se o PR pode ser aprovado pela app revisora (decisão do approver, política do time, tamanho, caminhos bloqueados, CI). Por padrão ele só comenta; aprovar e mergear é de uma pessoa |

## Pré-requisitos e instalação, por modo

Em todos os modos é preciso ter Go 1.27+ (para compilar), Docker (para o Postgres com pgvector),
git e o Claude Code logado numa assinatura. Os scanners (`golangci-lint`, `gosec`, `govulncheck`,
`semgrep`) são opcionais.

### Local: avaliar sem nenhuma chave

1. `git clone <repo do Sentinela> && cd sentinela && go build -o sentinela ./cmd/sentinela`
2. `gh auth login`, com acesso de escrita ao repo que você quer analisar.
3. `./sentinela init --mode local --repo sua-org/seu-repo`: sobe o Postgres, aplica as migrations,
   gera o `sentinela.yaml` mínimo e lista o que falta. Rode de novo até sair `pronto.`.
4. `./sentinela scan --all` e abra `reports/local/index.md`.
5. Decida com `./sentinela finding reject | accept | fix | resolve` e rode
   `./sentinela worker --once` para os pedidos de correção.

O roteiro com tempos e o que observar está em [avaliacao.md](avaliacao.md).

### MCP: o time decide no Linear, sem criar apps

1. No Claude Code, conecte o Linear (claude.ai > Settings > Connectors). `claude mcp list` deve
   mostrar `claude.ai Linear: ... ✔ Connected`.
2. `./sentinela init --mode mcp --repo sua-org/seu-repo`, com `--linear-team CHAVE` se houver mais
   de um time, e `--slack-channel C...` para avisos pelo MCP do Slack.
3. Crie no time do Linear as labels do `sentinela.yaml` (`sentinela`, `sentinela-fix`,
   `sentinela-done`, `sentinela-failed`) e rode `./sentinela config validate`.
4. Rode a primeira rodada em sombra: o `init` gera `publish.enabled: false`, então
   `./sentinela scan --all` grava os achados sem publicar nada no Linear. Revise com
   `./sentinela report --run <id>` o que sairia como issue.
5. Para publicar, troque para `publish.enabled: true` e rode `./sentinela scan --all` de novo: os
   achados pendentes saem no time do Linear, até `max_issues_per_team_per_run` por time.
6. Deixe `./sentinela serve` e `./sentinela worker` rodando. As decisões chegam com até
   `adapters.mcp.poll_interval` (15 min) de atraso. As ações saem com a sua conta.

### Completo: uso real, com identidade própria

1. Siga [instalacao.md](instalacao.md) (banco, binário, `.env`) e [integracoes.md](integracoes.md):
   - o app OAuth do Linear, com `sentinela linear auth`;
   - o bot do Slack, convidado nos canais;
   - as duas GitHub Apps.
2. Monte o `sentinela.yaml` principal e, se houver vários times, um arquivo por time em
   `config.d/teams/` (o cenário abaixo).
3. `./sentinela config validate` sem erros; depois `deploy/install.sh` para rodar sozinho
   ([operacao.md](operacao.md#rodando-sozinho)).

## Como o Sentinela decide time, projeto, canal e menção

Para cada caminho (o arquivo da evidência principal do achado), a ordem é a do fluxograma. O
`owners explain <repo> <caminho>` mostra cada passo e de onde veio cada valor.

```mermaid
flowchart TD
  A[caminho do achado] --> M{casa com um módulo da config?<br/>modules[].path ou config.d/teams/*.yaml}
  M -- sim: vence o glob mais longo --> T1[time do módulo]
  M -- não --> C{casa com o CODEOWNERS do repo?}
  C -- sim: vence a última regra que casa --> T2[time do dono<br/>teams.*.github ou members]
  C -- regra sem donos ou nenhuma --> R{repos[].team?}
  R -- sim --> T3[time do repo]
  R -- não --> F[fallback_team, senão defaults]
  T1 & T2 & T3 & F --> P{projeto}
  P --> P1[1. linear_project do módulo]
  P1 -.não tem.-> P2[2. category_projects do time<br/>pela categoria do achado, na publicação]
  P2 -.não tem.-> P3[3. linear.project do time]
  P3 -.não tem.-> P4[4. linear_project do repo]
  P4 -.não tem.-> P5[5. defaults.linear.project]
  T1 & T2 & T3 & F --> S[canal: notify.slack do time,<br/>senão defaults.notify.slack]
  T1 & T2 & T3 & F --> N[menção: notify.mention do time<br/>ID do grupo S..., senão ninguém]
```

Regras que valem a pena guardar:

- **Glob mais longo, ou override:** entre os módulos da config que casam com um caminho, vence o
  glob mais longo (o mais específico). Para arquivos transversais, que um time cuida em qualquer
  lugar do repo (Dockerfile, CI, infra, `go.mod`), não dependa do tamanho do glob: declare o módulo
  com `override: true`, como o `**/Dockerfile` da plataforma no cenário acme. Um override vence os
  módulos comuns; entre overrides, vale o mais longo (ADR 0043). O CODEOWNERS tem regra própria (a
  última que casa vence) e vem depois dos módulos da config.
- **Dono genérico:** quando o dono do arquivo principal vem de uma regra genérica (`*` do
  CODEOWNERS, `repos[].team`, `fallback_team`), vence um caminho mais específico do achado:
  `scope_paths`, depois as outras evidências, depois os pacotes citados (ADR 0017).
- **Roteamento gravado:** o roteamento fica gravado no achado. Mudar o CODEOWNERS depois não muda
  o passado. O projeto por categoria e o `defaults.linear.project` de último recurso são aplicados
  na publicação.
- **Menção:** só funciona com o ID do grupo (`S...`). Um handle (`@pagamentos-team`) sai como
  texto, porque o Slack só notifica grupos por ID.

## O cenário acme, montado do zero

A acme tem:

- **Monorepo `acme/plataforma`:** 10 módulos em `modules/<nome>`. Pagamentos tem 4 módulos (e
  `libs/dinheiro`), catálogo e identidade têm 2 cada, e a plataforma tem 2, mais os Dockerfiles, o
  CI e a infra.
- **Dois repos simples, de um time cada:** `acme/vitrine-web` (catálogo) e `acme/sdk-identidade`
  (identidade).
- **Cada time:** um time no Linear, um canal no Slack e um grupo para menção.
- **Pagamentos no Linear:** 5 projetos, um por módulo e um "Pagamentos · Segurança".

Os arquivos prontos estão em [`examples/monorepo-acme`](../examples/monorepo-acme); os passos abaixo
os montam na ordem em que alguém faria.

**Passo 1: o principal, com os administradores.** O `sentinela.yaml` tem só o que é da
organização:

- os adapters, os modelos e os custos;
- os padrões (`defaults`), com o canal `#sentinela` e o projeto de último recurso;
- `blocked_paths`;
- a auto-aprovação por time (`teams.plataforma.policy.auto_approve: true`);
- os repos compartilhados (o monorepo, com `fallback_team: plataforma`).

Veja o arquivo comentado linha a linha.

**Passo 2: um arquivo por time.** Cada time cria `config.d/teams/<time>.yaml` com o board, o
canal, a menção, os módulos e os repos dele. O de pagamentos:

```yaml
github: ["@acme/pagamentos"]
linear:
  team: PAG
  category_projects:
    security: "Pagamentos · Segurança"
notify:
  slack: C0ACMEPAG01                  # #pagamentos-alertas
  mention: S0ACMEPAG01                # @pagamentos-team
labels:
  trigger: pagamentos-fix
modules:
  - { repo: acme/plataforma, path: "modules/cobranca/**", linear_project: "Pagamentos · Cobrança" }
  - { repo: acme/plataforma, path: "modules/carteira/**", linear_project: "Pagamentos · Carteira" }
  - { repo: acme/plataforma, path: "modules/conciliacao-de-repasses/**", linear_project: "Pagamentos · Repasse" }
  - { repo: acme/plataforma, path: "modules/fraude/**",   linear_project: "Pagamentos · Fraude" }
```

Catálogo e identidade não precisam listar os módulos do monorepo, porque o CODEOWNERS já diz quem
é dono e eles usam o projeto do time. Eles listam os seus repos inteiros em `repos:`. A plataforma
declara `**/Dockerfile` com `override: true` para ficar com o Dockerfile de qualquer lugar,
inclusive dentro dos módulos de outros times.

**Passo 3: o CODEOWNERS de cada repo.** No monorepo
([repos/plataforma/.github/CODEOWNERS](../examples/monorepo-acme/repos/plataforma/.github/CODEOWNERS)):

- `*` para a plataforma;
- uma linha por módulo;
- `/libs/comum/` com dois donos;
- `/docs/rascunhos/` sem donos.

Nos repos simples, `* @acme/<time>`.

**Passo 4: quem aprova o quê no repo da config.** O
[`.github/CODEOWNERS`](../examples/monorepo-acme/.github/CODEOWNERS) do repo onde ficam o
`sentinela.yaml` e o `config.d` dá cada arquivo de time ao seu time e o principal aos
administradores. O
[workflow](../examples/monorepo-acme/.github/workflows/sentinela-config.yml) roda o `validate`
offline em cada PR e comenta o resultado.

**Passo 5: conferir.**

```bash
sentinela --config examples/monorepo-acme/sentinela.yaml config validate --offline
```

Mostra os times de cada arquivo e o projeto, o canal e a menção efetivos de cada time, com a
fonte, e termina em `0 erro(s)`. Com Linear e Slack de verdade, `config validate` (sem
`--offline`) também confere se os times, os estados, os projetos e as labels existem no Linear e se
o bot está em cada canal.

**Passo 6: ver o roteamento.** As saídas abaixo são reais, do binário atual, com o CODEOWNERS
local (`--codeowners`). O teste `TestAcmeExampleValidates` garante que continuam assim.

**Módulo de pagamentos, com projeto próprio.** O passo 1 (config) casa primeiro, e o projeto vem do módulo. A linha "por categoria" lembra que um achado de segurança aqui continua no projeto do módulo, que vence a categoria.

```
$ sentinela owners explain acme/plataforma modules/cobranca/fatura.go
acme/plataforma modules/cobranca/fatura.go
  1. owners_source=both
  2. passo 1: modules/cobranca/** casou (glob mais específico) -> time pagamentos
resultado:
  time:            pagamentos
  regra:           config:modules/cobranca/**
  linear:          PAG / "Pagamentos · Cobrança" / estado "Triage"
  projeto:         Pagamentos · Cobrança (de repos[acme/plataforma].modules[modules/cobranca/**].linear_project (config.d/teams/pagamentos.yaml))
  por categoria:   security -> Pagamentos · Segurança (teams.pagamentos.linear.category_projects; vale se o projeto não vier de um módulo)
  assignee:        lead lider.pagamentos@example.com
  canal:           C0ACMEPAG01 (de teams.pagamentos.notify.slack (config.d/teams/pagamentos.yaml))
  menção:          S0ACMEPAG01 (de teams.pagamentos.notify.mention (config.d/teams/pagamentos.yaml))
  política:        auto_approve=false blocked=true
```

**Dockerfile dentro de um módulo de outro time.** O módulo `**/Dockerfile` da plataforma tem
`override: true`, então vence o `modules/cobranca/**` de pagamentos, que é mais longo. Sem o
override, valeria o glob mais longo, e o Dockerfile seria de pagamentos.

```
acme/plataforma modules/cobranca/Dockerfile
  1. owners_source=both
  2. passo 1: **/Dockerfile casou (override: vence os módulos comuns) -> time plataforma
resultado:
  time:            plataforma
  regra:           config:**/Dockerfile
  linear:          PLAT / "Plataforma" / estado "Triage"
  projeto:         Plataforma (de teams.plataforma.linear.project (config.d/teams/plataforma.yaml))
  assignee:        none 
  canal:           C0ACMEPLAT1 (de teams.plataforma.notify.slack (config.d/teams/plataforma.yaml))
  menção:          S0ACMEPLAT1 (de teams.plataforma.notify.mention (config.d/teams/plataforma.yaml))
  política:        auto_approve=true blocked=-
```

**Regra genérica perdendo para uma específica.** O `*` da linha 6 do CODEOWNERS também casa, mas a semântica do GitHub é "a última regra que casa vence", e `/modules/busca/` (linha 17) vem depois.

```
$ sentinela owners explain acme/plataforma modules/busca/indice.go
acme/plataforma modules/busca/indice.go
  1. owners_source=both
  2. passo 1: nenhum modules[].path casou
  3. passo 2: owner @acme/catalogo -> time catalogo (teams.catalogo.github)
  4. passo 2: regra L17 (/modules/busca/) casou, última que casa vence
resultado:
  time:            catalogo
  regra:           codeowners:L17
  linear:          CAT / "Catálogo" / estado "Triage"
  projeto:         Catálogo (de teams.catalogo.linear.project (config.d/teams/catalogo.yaml))
  assignee:        none 
  canal:           C0ACMECAT01 (de teams.catalogo.notify.slack (config.d/teams/catalogo.yaml))
  menção:          S0ACMECAT01 (de teams.catalogo.notify.mention (config.d/teams/catalogo.yaml))
  política:        auto_approve=false blocked=-
```

**Arquivo compartilhado, com dois donos.** O primeiro dono da regra é o principal; o segundo entra como inscrito na issue.

```
$ sentinela owners explain acme/plataforma libs/comum/moeda.go
acme/plataforma libs/comum/moeda.go
  1. owners_source=both
  2. passo 1: nenhum modules[].path casou
  3. passo 2: owner @acme/plataforma -> time plataforma (teams.plataforma.github)
  4. passo 2: owner @acme/pagamentos -> time pagamentos (teams.pagamentos.github)
  5. passo 2: regra L32 (/libs/comum/) casou, última que casa vence
resultado:
  time:            plataforma
  regra:           codeowners:L32
  linear:          PLAT / "Plataforma" / estado "Triage"
  projeto:         Plataforma (de teams.plataforma.linear.project (config.d/teams/plataforma.yaml))
  assignee:        none 
  inscritos:       pagamentos
  canal:           C0ACMEPLAT1 (de teams.plataforma.notify.slack (config.d/teams/plataforma.yaml))
  menção:          S0ACMEPLAT1 (de teams.plataforma.notify.mention (config.d/teams/plataforma.yaml))
  política:        auto_approve=true blocked=-
```

**Código de pagamentos fora dos módulos.** Sem projeto de módulo nem do time, o projeto cai em `defaults.linear.project`. Um achado de segurança aqui iria para "Pagamentos · Segurança", porque a categoria vence time, repo e defaults.

```
$ sentinela owners explain acme/plataforma libs/dinheiro/arredonda.go
acme/plataforma libs/dinheiro/arredonda.go
  1. owners_source=both
  2. passo 1: nenhum modules[].path casou
  3. passo 2: owner @acme/pagamentos -> time pagamentos (teams.pagamentos.github)
  4. passo 2: regra L13 (/libs/dinheiro/) casou, última que casa vence
resultado:
  time:            pagamentos
  regra:           codeowners:L13
  linear:          PAG / "Sentinela · Geral" / estado "Triage"
  projeto:         Sentinela · Geral (de defaults.linear.project)
  por categoria:   security -> Pagamentos · Segurança (teams.pagamentos.linear.category_projects; vale se o projeto não vier de um módulo)
  assignee:        lead lider.pagamentos@example.com
  canal:           C0ACMEPAG01 (de teams.pagamentos.notify.slack (config.d/teams/pagamentos.yaml))
  menção:          S0ACMEPAG01 (de teams.pagamentos.notify.mention (config.d/teams/pagamentos.yaml))
  política:        auto_approve=false blocked=true
```

**CI.** `/.github/` é da plataforma.

```
$ sentinela owners explain acme/plataforma .github/workflows/ci.yml
acme/plataforma .github/workflows/ci.yml
  1. owners_source=both
  2. passo 1: nenhum modules[].path casou
  3. passo 2: owner @acme/plataforma -> time plataforma (teams.plataforma.github)
  4. passo 2: regra L26 (/.github/) casou, última que casa vence
resultado:
  time:            plataforma
  regra:           codeowners:L26
  linear:          PLAT / "Plataforma" / estado "Triage"
  projeto:         Plataforma (de teams.plataforma.linear.project (config.d/teams/plataforma.yaml))
  assignee:        none 
  canal:           C0ACMEPLAT1 (de teams.plataforma.notify.slack (config.d/teams/plataforma.yaml))
  menção:          S0ACMEPLAT1 (de teams.plataforma.notify.mention (config.d/teams/plataforma.yaml))
  política:        auto_approve=true blocked=-
```

**Infra compartilhada.** `/infra/` é da plataforma, e `infra/**` está em `blocked_paths`: nada aqui é auto-aprovado.

```
$ sentinela owners explain acme/plataforma infra/terraform/rede.tf
acme/plataforma infra/terraform/rede.tf
  1. owners_source=both
  2. passo 1: nenhum modules[].path casou
  3. passo 2: owner @acme/plataforma -> time plataforma (teams.plataforma.github)
  4. passo 2: regra L27 (/infra/) casou, última que casa vence
resultado:
  time:            plataforma
  regra:           codeowners:L27
  linear:          PLAT / "Plataforma" / estado "Triage"
  projeto:         Plataforma (de teams.plataforma.linear.project (config.d/teams/plataforma.yaml))
  assignee:        none 
  canal:           C0ACMEPLAT1 (de teams.plataforma.notify.slack (config.d/teams/plataforma.yaml))
  menção:          S0ACMEPLAT1 (de teams.plataforma.notify.mention (config.d/teams/plataforma.yaml))
  política:        auto_approve=true blocked=-
```

**Caminho sem dono.** A regra `/docs/rascunhos/` não tem donos; o roteamento passa do CODEOWNERS para `repos[].team` (vazio) e cai no `fallback_team`. Sem `fallback_team`, ele cairia em `defaults`, sem time; o `config validate` avisa e mostra exemplos de arquivos nessa situação.

```
$ sentinela owners explain acme/plataforma docs/rascunhos/notas.md
acme/plataforma docs/rascunhos/notas.md
  1. owners_source=both
  2. passo 1: nenhum modules[].path casou
  3. passo 2: regra L35 (/docs/rascunhos/) casou sem donos; caminho sem dono no CODEOWNERS
  4. passo 3: repo sem team
  5. passo 4: repos[].fallback_team = plataforma
resultado:
  time:            plataforma
  regra:           fallback
  linear:          PLAT / "Plataforma" / estado "Triage"
  projeto:         Plataforma (de teams.plataforma.linear.project (config.d/teams/plataforma.yaml))
  assignee:        none 
  canal:           C0ACMEPLAT1 (de teams.plataforma.notify.slack (config.d/teams/plataforma.yaml))
  menção:          S0ACMEPLAT1 (de teams.plataforma.notify.mention (config.d/teams/plataforma.yaml))
  política:        auto_approve=true blocked=-
```

**Só a regra genérica.** Nada mais específico casa com o README da raiz: vale o `*`, da plataforma.

```
$ sentinela owners explain acme/plataforma README.md
acme/plataforma README.md
  1. owners_source=both
  2. passo 1: nenhum modules[].path casou
  3. passo 2: owner @acme/plataforma -> time plataforma (teams.plataforma.github)
  4. passo 2: regra L6 (*) casou, última que casa vence
  5. passo 2: L6 (*) é regra genérica
resultado:
  time:            plataforma
  regra:           codeowners:L6
  linear:          PLAT / "Plataforma" / estado "Triage"
  projeto:         Plataforma (de teams.plataforma.linear.project (config.d/teams/plataforma.yaml))
  assignee:        none 
  canal:           C0ACMEPLAT1 (de teams.plataforma.notify.slack (config.d/teams/plataforma.yaml))
  menção:          S0ACMEPLAT1 (de teams.plataforma.notify.mention (config.d/teams/plataforma.yaml))
  política:        auto_approve=true blocked=-
```

**Repo de um time só.** O `vitrine-web` é do catálogo pelo CODEOWNERS dele (`* @acme/catalogo`). O projeto vem do time: como o time define um, o `linear_project` do repo não valeria (time > repo).

```
$ sentinela owners explain acme/vitrine-web src/App.tsx
acme/vitrine-web src/App.tsx
  1. owners_source=both
  2. passo 1: nenhum modules[].path casou
  3. passo 2: owner @acme/catalogo -> time catalogo (teams.catalogo.github)
  4. passo 2: regra L2 (*) casou, última que casa vence
  5. passo 2: L2 (*) é regra genérica
resultado:
  time:            catalogo
  regra:           codeowners:L2
  linear:          CAT / "Catálogo" / estado "Triage"
  projeto:         Catálogo (de teams.catalogo.linear.project (config.d/teams/catalogo.yaml))
  assignee:        none 
  canal:           C0ACMECAT01 (de teams.catalogo.notify.slack (config.d/teams/catalogo.yaml))
  menção:          S0ACMECAT01 (de teams.catalogo.notify.mention (config.d/teams/catalogo.yaml))
  política:        auto_approve=false blocked=-
```

### Achado que cruza times

Há dois mecanismos diferentes, e eles convivem:

| | "Também afeta" (ADR 0044) | Issue relacionada (System Architect) |
|---|---|---|
| De onde vem | As **evidências** do achado tocam módulos de outros times | O **Architect**, que olha o repo inteiro, cita módulos afetados (`related_modules`) |
| O que cria | Nada: só a seção na issue do dono e a menção do grupo no aviso | Uma **issue no board do outro time**, linkada à principal |
| Quando usar | O outro time precisa saber, mas a correção é do dono | O outro time tem trabalho próprio a fazer |
| Repetição | Um time que já ganhou issue relacionada não entra em "Também afeta" | — |

O dono é um só. Quando as outras evidências tocam módulos de outros times, a issue ganha a seção
"Também afeta", e o aviso no canal do dono menciona o grupo de cada time secundário. Nenhuma issue
é criada para eles (ADR 0044). Com mais de um caminho, o `owners explain` mostra os secundários:

```
$ sentinela owners explain acme/plataforma modules/cobranca/fatura.go modules/contas/login.go modules/cobranca/juros.go
acme/plataforma modules/cobranca/fatura.go
  1. owners_source=both
  2. passo 1: modules/cobranca/** casou (glob mais específico) -> time pagamentos
resultado:
  time:            pagamentos
  regra:           config:modules/cobranca/**
  linear:          PAG / "Pagamentos · Cobrança" / estado "Triage"
  projeto:         Pagamentos · Cobrança (de repos[acme/plataforma].modules[modules/cobranca/**].linear_project (config.d/teams/pagamentos.yaml))
  por categoria:   security -> Pagamentos · Segurança (teams.pagamentos.linear.category_projects; vale se o projeto não vier de um módulo)
  assignee:        lead lider.pagamentos@example.com
  canal:           C0ACMEPAG01 (de teams.pagamentos.notify.slack (config.d/teams/pagamentos.yaml))
  menção:          S0ACMEPAG01 (de teams.pagamentos.notify.mention (config.d/teams/pagamentos.yaml))
  política:        auto_approve=false blocked=true
  secundário:      identidade (módulo /modules/contas/: modules/contas/login.go; menção S0ACMEIDN01 no aviso do dono)
```

## Customizar: o que é do time e o que é da organização

| O quê | Onde | Quem decide |
|---|---|---|
| Board, projeto, estados e labels extras do Linear | `linear` do time | time |
| Projeto por módulo | `modules[].linear_project` | time |
| Projeto por categoria (ex.: segurança) | `linear.category_projects` do time | time |
| Canal e menção no Slack | `notify.slack`, `notify.mention` do time | time |
| Label de gatilho, de PR aberto e de falha | `labels.trigger`, `labels.done`, `labels.failed` do time; as da organização em `labels` | time (as dele), organização (as padrão) |
| Tamanho máximo de PR para o gate | `policy.max_lines`, `policy.max_files` | time pode apertar; organização define o padrão em `defaults.policy` |
| Domínio sensível (nunca auto-aprova) | `policy.blocked: true` | time ou organização |
| Caminhos bloqueados | `blocked_paths` | organização; o time só acrescenta |
| Auto-aprovação | `policy.auto_approve` em `defaults` ou `teams.<time>` do principal | organização |
| Revisora que aprova | `github_apps.reviewer_approves` | organização |
| Modelos e custos | `models`, `teams.<time>.models` e `repos[].models` no principal, `budget`, `pricing` | organização |
| Modo de operação | `adapters` | organização |

Num arquivo de time, as chaves da organização são recusadas pelo `config validate`, com a chave, o
arquivo e o porquê. Para a organização dar uma política a um time que mora em `config.d`, ela
declara no principal só `teams.<time>.policy` (e `models`). O cenário acme faz isso para liberar a
auto-aprovação da plataforma.

## Migrar sem perder o histórico

O histórico (achados, decisões, supressões, exemplos do Curator, execuções e métricas) fica no
Postgres e não depende do formato da config nem do modo.

- **De um arquivo só para `config.d`:**
  1. Para cada time, crie `config.d/teams/<time>.yaml` com o bloco dele de `teams` (sem `policy`
     e `models`) e com os módulos dele, tirados de `repos[].modules` e escritos como
     `{ repo: <repo>, path: ..., linear_project: ... }`.
  2. Repos inteiros de um time podem ir para `repos:` do arquivo dele.
  3. No principal, deixe em `teams.<time>` só `policy` e `models`.
  4. Rode `sentinela config validate --offline --changed config.d/teams/<time>.yaml` e compare o
     antes e o depois: o roteamento tem de sair igual.
  5. Rode `sentinela owners pending`: nenhum achado pendente deve mudar de projeto.

  Os serviços releem a config no ciclo seguinte; não é preciso reiniciar.
- **Entre modos:** troque `adapters` e reinicie os serviços. O passo a passo de cada transição
  (local para MCP para completo) está em [modos.md](modos.md#migrar-entre-modos-sem-perder-o-histórico).

## Erros comuns

As mensagens abaixo são as do `config validate` atual.

| Mensagem | Causa | Correção |
|---|---|---|
| `config.d/teams/pagamentos.yaml: chave "models" só vale no sentinela.yaml principal: modelos e custos são da organização` | Chave da organização no arquivo do time | Tire a chave do arquivo do time; se a organização quer, ela põe em `teams.pagamentos.models` no principal |
| `config.d/teams/pagamentos.yaml: chave "policy.auto_approve" só vale no sentinela.yaml principal: auto-aprovação é decisão da organização (defaults.policy ou teams.<time>.policy no principal)` | O time tentou se auto-aprovar | Peça aos administradores; eles põem `teams.pagamentos.policy.auto_approve` no principal |
| `repos[acme/plataforma]: o módulo "modules/cobranca/**" está em dois times: catalogo (config.d/teams/catalogo.yaml) e pagamentos (config.d/teams/pagamentos.yaml); cada caminho tem um dono só` | Dois times declararam o mesmo módulo | Combine qual time é o dono; o outro tira a linha. Para dividir, use globs diferentes (o mais longo vence) |
| `config.d/teams/catalogo.yaml: yaml: unmarshal errors: line 18: field canal not found in type config.TeamFile` | Chave que não existe (aqui, `canal` em vez de `notify.slack`) | Use as chaves de `schema/team.schema.json`; o editor com o schema avisa na hora |
| `config.d/teams/catalogo.yaml: modules: o repo acme/loja não está no sentinela.yaml (o principal declara os repos compartilhados)` | Módulo num repo que ninguém declarou | Peça para o repo entrar no principal ou, se ele for só do time, declare-o em `repos:` do arquivo do time |
| `config.d/teams/catalogo2.yaml: o repo acme/vitrine-web já está no sentinela.yaml ou em outro arquivo de time; um repo inteiro tem um time só (para dividir por módulo, use modules)` | O mesmo repo inteiro em dois lugares | Deixe o repo num lugar só |
| `config.d/teams/catalogo.yaml: o time "catalogo" já está em teams do sentinela.yaml com chaves do time; no principal, teams.catalogo só pode ter policy, models e fix_trigger (a parte da organização), o resto fica neste arquivo` | O time foi declarado nos dois arquivos | No principal, deixe só `policy`, `models` e `fix_trigger` |
| `repos acme/plataforma.fallback_team: time "plataformas" não existe em teams` | Nome de time errado | Use o nome do arquivo do time (sem `.yaml`) ou a chave em `teams` |
| `teams.pagamentos.linear.category_projects: categoria "seguranca" não existe (use bug, performance, improvement, security) (config.d/teams/pagamentos.yaml)` | Categoria em português | Use `security` |
| `teams.pagamentos.labels.trigger: label "claude-fix" é de outro sistema (claude-*); o Sentinela usa só as suas` | Label reservada | Escolha outro nome |
| `teams.pagamentos.labels.trigger: label "pagamentos-fix" não existe no time PAG do Linear` | A label do time não foi criada no Linear (só no `validate` sem `--offline`) | Crie a label no time do Linear |
| `teams.financeiro.notify: o bot não está no canal C0FINANCE01 (...); convide o app no canal` | O bot do Slack não foi convidado (sem `--offline`) | `/invite @Sentinela` no canal |
| `teams.pagamentos: linear.project "Pagamentos · Cobrança" é também o projeto de um módulo do time; com category_projects, o Sentinela não distingue os dois na publicação: use projetos diferentes` | Projeto do time igual ao de um módulo, com projeto por categoria | Dê ao time outro projeto (ou nenhum) |
