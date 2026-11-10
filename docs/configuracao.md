# Configuração: `sentinela.yaml`

Uma instalação é configurada pelo `sentinela.yaml`, e opcionalmente por um arquivo por time em
`config.d/teams/<time>.yaml` ao lado dele (ADR 0041; veja [`config.d/teams`](#configdteamstimeyaml)).
Comece por [`sentinela.example.yaml`](../sentinela.example.yaml) ou pelo cenário completo em
[`examples/monorepo-acme`](../examples/monorepo-acme). O guia passo a passo é
[guia-configuracao.md](guia-configuracao.md). Segredos vão no `.env`, nunca aqui. Para o editor
completar e validar as chaves, use o JSON Schema em `schema/sentinela.schema.json` e
`schema/team.schema.json` (gerados com `sentinela config schema`).

```bash
cp sentinela.example.yaml sentinela.yaml
./sentinela config validate --offline   # estrutura, política e roteamento, sem chamar ninguém
./sentinela config validate             # confere também Linear, GitHub, Slack, Apps e banco
```

Durações usam o formato do Go (`30s`, `10m`, `168h`). Campos omitidos assumem o padrão indicado.

## Exemplo mínimo: um repo de um time

```yaml
defaults:
  linear: { team: PLAT, labels: [sentinela], triage_state: Triage }
  notify: { slack: C0XXXXXXXXX }
teams:
  platform:
    github: ["@acme/platform"]
    linear: { team: PLAT, project: "Sentinela" }
repos:
  - name: acme/app-java-angular
    team: platform
# perfil qualidade (padrão); o economia está em docs/custos.md#perfis-de-modelos
models: { triage: claude-opus-5-5, analysis: claude-opus-5-5, architect: claude-opus-5-5, judge: claude-sonnet-5-5, curator: claude-sonnet-5-5,
          notes: claude-sonnet-5-5, orchestrator: claude-sonnet-5-5, spec_writer: claude-sonnet-5-5, executor: claude-sonnet-5-5,
          reviewer_security: claude-opus-5-5, reviewer_code: claude-sonnet-5-5, reviewer_reliability: claude-sonnet-5-5,
          reviewer_domain: claude-sonnet-5-5, approver: claude-opus-5-5, shepherd: claude-sonnet-5-5 }
pricing:
  models:
    claude-haiku-5-5: { input: 0.10, output: 0.50 }
    claude-sonnet-5-5: { input: 2.00, output: 10.00 }
    claude-opus-5-5: { input: 4.00, output: 20.00 }
budget: { per_run_usd: 10, per_pr_usd: 3 }
publish: { enabled: false }
```

## Exemplo: monorepo com um time por módulo

Trecho com `teams` e `repos`; o resto é igual ao exemplo mínimo.

```yaml
teams:
  payments:
    github: ["@acme/payments"]
    linear: { team: PAY, project: "Sentinela · Payments" }
    assignee: lead
    lead: lead.payments@example.com
    notify: { slack: C0PAYMENTS1, mention: S0PAYTEAM1 }
    policy: { auto_approve: false, blocked: true }
  platform:
    github: ["@acme/platform", "@acme/sre"]
    members: [fulano]
    linear: { team: PLAT, project: "Sentinela · Platform" }
    assignee: round_robin
repos:
  - name: acme/monorepo
    owners_source: both
    modules:
      - { path: "services/billing/**", team: payments }
      - { path: "libs/**", team: platform }
      - { path: "**/Dockerfile", team: platform }
      - { path: "services/legacy/**", ignore: true }
    fallback_team: platform
```

Neste exemplo:
- `services/billing/` vai para o board PAY e nunca é auto-aprovado.
- `libs/` e qualquer `Dockerfile` vão para a plataforma.
- `services/legacy/` fica fora da análise.
- O resto segue o CODEOWNERS do repo e, sem dono, cai na plataforma.

## Roteamento de ownership

Para cada caminho, o dono é decidido nesta ordem:

1. `repos[].modules[].path` (ou `modules` de `config.d/teams/<time>.yaml`): vence o glob mais
   longo, e um módulo com `override: true` vence os comuns (ADR 0043). É pulado com
   `owners_source: codeowners`.
2. `CODEOWNERS` do repo, com a semântica do GitHub (a última regra que casa vence). O dono vira
   time por `teams[].members` (usuário individual) ou `teams[].github`. É pulado com
   `owners_source: config`.
3. `repos[].team`.
4. `repos[].fallback_team`, depois `defaults`.

O **projeto do Linear** segue a ordem módulo (`modules[].linear_project`) > categoria do time
(`teams.<nome>.linear.category_projects`, escolhida na publicação) > time > repo >
`defaults.linear.project` (ADR 0040). O **canal** é o do time, senão `defaults.notify.slack`; a
**menção** é a do time. O `owners explain` e o `config validate` mostram cada um e de onde veio.

Para um **achado**, vale o dono do arquivo da evidência principal. A exceção é quando ele vem de
uma regra genérica (`*` do CODEOWNERS, `repos[].team`, `fallback_team` ou `defaults`). Nesse caso,
vence o caminho mais específico, procurando nesta ordem:
- `scope_paths`;
- as demais evidências;
- os pacotes que o código cita (em Go, `pkg.Ident` resolvido pelos imports).

Quando as evidências de um achado tocam módulos de outros times, a issue do dono ganha a seção
"Também afeta" e o aviso menciona o grupo desses times, sem issue duplicada (ADR 0044). Os módulos
que o System Architect cita (`related_modules`) geram uma issue relacionada no board de cada outro
time afetado. Para ver o
que decidiu um caminho:

```bash
./sentinela owners explain acme/monorepo services/billing/invoice.go
```

Para gerar um rascunho de `teams` e `repos` a partir do CODEOWNERS e dos times do Linear:

```bash
./sentinela config bootstrap --repo acme/monorepo
```

## Referência

### `workspace`

| Campo | Descrição |
|---|---|
| `linear_workspace`, `slack_workspace`, `github_owner` | Informativos, para quem lê o arquivo |

### `defaults`

| Campo | Padrão | Descrição |
|---|---|---|
| `linear.team` | — | Time do Linear usado quando nada mais casar |
| `linear.project` | — | Projeto de último recurso: vale quando nem o time (`teams.<t>.linear.project`) nem o repo (`repos[].linear_project`) definem um. Também vale na publicação de achados gravados sem projeto. O `owners explain` e o `config validate` mostram o projeto efetivo e de onde veio; o `owners pending` mostra o de cada achado pendente |
| `linear.labels` | — | Labels de toda issue |
| `linear.triage_state` | — | Estado inicial das issues; precisa existir no time |
| `linear.review_state` | — | Estado para onde a issue vai quando o worker abre o PR (ex.: `In Review`); vazio não move (ADR 0027) |
| `linear.done_state` | `Done` | Estado para onde a issue vai quando o worker vê o PR mergeado; precisa existir no time (ADR 0028) |
| `owners_source` | `both` | `codeowners`, `config` ou `both` (config tem precedência) |
| `notify.slack` | — | Canal padrão: resumo de cada rodada e times sem canal próprio |
| `policy` | — | Política padrão do Policy Gate (veja `teams[].policy`) |

### `teams.<nome>`

| Campo | Descrição |
|---|---|
| `github` | Como o time aparece no CODEOWNERS (`@org/time`) |
| `members` | Usuários individuais do CODEOWNERS que pertencem ao time |
| `linear.team`, `linear.project`, `linear.triage_state`, `linear.review_state`, `linear.done_state`, `linear.labels` | Board, projeto, estado inicial, estado com PR aberto, estado com PR mergeado e labels extras do time |
| `assignee` | `none`, `lead` (usa `lead`) ou `round_robin` (alterna entre `members` com e-mail) |
| `lead` | E-mail de quem recebe as issues com `assignee: lead` |
| `notify.slack` | Canal do time: ID `C...` (preferível) ou `#nome` |
| `notify.mention` | Quem mencionar: user group `S...`, usuário `U...` ou `@here`. Um handle (`@grupo`) sai como texto |
| `policy.auto_approve` | O gate pode aprovar PRs deste time (só com `github_apps.reviewer_approves`) |
| `policy.blocked` | Domínio sensível: nunca auto-aprova |
| `policy.max_lines`, `policy.max_files` | Limites de tamanho do PR para o gate |
| `models` | Só os papéis que mudam para este time (veja [`models`](#models)) |
| `linear.category_projects` | Projeto por categoria do time (`bug`, `performance`, `improvement`, `security`), ex.: `security: "Pagamentos · Segurança"`. Vale na publicação e perde para o projeto do módulo (ADR 0040) |
| `labels.trigger`, `labels.done`, `labels.failed` | Troca, só para este time, a label de gatilho, a de PR aberto e a de falha (ADR 0038). Vazio usa a de [`labels`](#labels). O `config validate` confere se existem no time do Linear |

### `adapters`

As pontas do Sentinela (ADR 0038). O Postgres é obrigatório e o mesmo em todos os modos: só mudam
a saída dos achados, a entrada das decisões, os avisos e o host git.

| Campo | Padrão | Valores |
|---|---|---|
| `tracker` | `linear_api` | `linear_api`: issues no Linear pela API (app OAuth) e decisões pelo sync. `linear_mcp`: o mesmo pelo MCP do Linear do Claude Code do usuário, como ele (ADR 0039). `local`: achados em markdown e HTML em `local_dir`, decisões por `sentinela finding accept\|reject\|fix\|resolve` |
| `notifier` | `slack_api` | `slack_api`: bot do Slack. `slack_mcp`: o MCP do Slack do usuário, como ele, com uma chamada por aviso. `none`: sem avisos |
| `git_host` | `github_apps` | `github_apps`: as duas GitHub Apps. `gh_cli`: a conta do `gh` logado abre o PR em draft, e a revisão do gate sai só como comentário |
| `local_dir` | `reports/local` | Onde o tracker local escreve um relatório por repo e o `index.md` |
| `mcp.linear_server`, `mcp.slack_server` | `claude.ai Linear`, `claude.ai Slack` | Nomes dos servidores em `claude mcp list` |
| `mcp.poll_interval` | `15m` | Intervalo do sync e validade do estado lido das issues no `linear_mcp`: cada leitura é uma chamada `claude -p` |
| `mcp.model` | o mais barato de `pricing.models` | Modelo das chamadas aos MCPs |

O `linear_mcp` exige `trigger.mode: polling`, porque sem app não há webhook. Veja os três modos em
[modos.md](modos.md).

`sentinela init --mode local` gera um `sentinela.yaml` com `tracker: local`, `notifier: none` e
`git_host: gh_cli`. Nos dois modos, e no `config bootstrap`, `discovery.scanners` sai pelas
linguagens dos repos (do clone do scan, se já existe, ou de `GET /repos/{repo}/languages` no
GitHub, com as linguagens de ao menos 1% do código): Go dá `golangci-lint`, `gosec` e `govulncheck`;
TypeScript e JavaScript, `eslint`, `tsc` e `osv-scanner`; Java e Kotlin, `osv-scanner`; o
`semgrep` entra sempre, e um comentário lista os pacotes de regras das linguagens. Sem as
linguagens de algum repo, a lista sai completa (os que não se aplicam aparecem como pulados). O
`config validate` avisa quando o clone de um repo tem uma linguagem cujo scanner está instalado e
fora da lista. `sentinela init --mode mcp` gera o mesmo com `tracker: linear_mcp`, o time do
Linear e `publish.enabled: false` (modo sombra: a primeira rodada só grava; ligue depois de
revisar com `sentinela report`), e com `--slack-channel` usa `notifier: slack_mcp`.

### `repos[]`

| Campo | Padrão | Descrição |
|---|---|---|
| `name` | — | `owner/repo` no GitHub |
| `default_branch` | `main` | Branch analisada e base dos PRs |
| `languages` | — | Informativo; a linguagem de cada pacote é detectada pelos arquivos |
| `linear_project` | — | Projeto do Linear do repo (usado quando o time não define projeto) |
| `owners_source` | o de `defaults` | Como acima, por repo |
| `team` | — | Time dono do repo inteiro (repo de um time só) |
| `modules[]` | — | `path` (glob), `team` ou `ignore: true`, `linear_project` (projeto do módulo, que vence o do time; ADR 0040) e `override: true` (vence os módulos comuns que também casam, para arquivos transversais como `**/Dockerfile`; ADR 0043) |
| `fallback_team` | — | Time para caminhos sem dono. Sem ele (nem `team`), o que nenhum módulo nem o CODEOWNERS cobre (arquivos da raiz, CI, compose) cai em `defaults`, sem time, e o `config validate` avisa: com o clone do scan, lista os arquivos; sem ele, avisa pela config |
| `models` | — | Só os papéis que mudam para este repo (veja [`models`](#models)) |

### `labels`

| Campo | Exemplo | Quem aplica |
|---|---|---|
| `finding` | `sentinela` | Sentinela, em toda issue |
| `trigger` | `sentinela-fix` | Uma pessoa: pede a correção |
| `done` | `sentinela-done` | Sentinela: PR em draft aberto |
| `failed` | `sentinela-failed` | Sentinela: a linha de execução falhou |
| `question` | `sentinela-pergunta` | Sentinela: achado que depende de decisão de produto |
| `intentional` | `sentinela-intencional` | Uma pessoa: "é intencional", vira supressão |
| `not_intentional` | `sentinela-nao-intencional` | Uma pessoa: "não é intencional", vira achado normal |

A coluna do meio é o valor do exemplo: só `finding` tem padrão (`sentinela`); as outras ficam
vazias se não forem definidas, e a parte do fluxo que depende delas não acontece. Labels com
prefixo `claude-` são recusadas: pertencem a outro sistema. `trigger`, `done` e `failed` podem ser trocadas por time em
`teams.<nome>.labels`.

### `fix_trigger`

Quem pode pedir a correção com a label de gatilho, e de quem os comentários entram como
"Instruções da equipe" (ADR 0042). É política da organização: vale no principal, e por time em
`teams.<nome>.fix_trigger`, nunca num arquivo de `config.d/teams`.

| Campo | Padrão | Valores |
|---|---|---|
| `allowed` | `anyone` (sem a chave; o `config validate` avisa) | `anyone`, `team_members` (membros do time do Linear dono do achado) ou uma lista de usuários do Linear (e-mail ou nome), que pode incluir `team_members` |

Um gatilho recusado não mexe na issue nem na label: o Sentinela comenta uma vez, registra e espera
alguém autorizado aplicar a label de novo. No modo MCP, só as instruções são filtradas (o MCP não
mostra quem aplicou a label). `sentinela linear members <time>` lista os membros.

### `github_apps`

| Campo | Padrão | Descrição |
|---|---|---|
| `bot` | — | Slug da app que abre PRs |
| `reviewer` | — | Slug da app revisora |
| `reviewer_approves` | `false` | Com `false`, a revisora só comenta. Ligar é decisão do dono da instalação |

### `budget`

| Campo | Descrição |
|---|---|
| `per_run_usd` | Teto da rodada de descoberta (só `anthropic_api`) |
| `per_pr_usd` | Teto padrão de um job da linha de execução |
| `per_month_usd` | Referência mensal |

### `models`

Um ID de modelo por papel:
- **Descoberta:** `triage`, `analysis`, `architect`, `judge`, `curator`, `notes` (intérprete de
  notas de review; sem a chave, usa o de `analysis`).
- **Linha de execução:** `orchestrator` (a sessão da skill `sentinela-fix`, que coordena os
  subagentes; sem a chave, usa o de `spec_writer`), `spec_writer`, `executor` (builder),
  `reviewer_security`, `reviewer_code`, `reviewer_reliability`, `reviewer_domain`, `approver`.
- **Shepherd:** `shepherd` (padrão: `reviewer_code`).

O Policy Gate é código, sem modelo.

**Perfis.** O bloco é sempre explícito. `init` e `config bootstrap` o escrevem completo a partir
de um perfil (`--models qualidade|economia|personalizado`; qualidade é o padrão), com o perfil
num comentário. O `config models` mostra a que perfil cada papel corresponde. Os papéis de cada
perfil estão em [custos.md](custos.md#perfis-de-modelos) (ADR 0051).

Também ficam aqui:
- `refusal.action` (`fallback_model` ou `skip`) e `refusal.fallback_model`: o que fazer quando o
  modelo recusa;
- `use_batch`: usa a Batch API (só `anthropic_api`).

**Modelo por repo e por time.** Cada papel pode mudar para um repo (`repos[].models`) ou para um
time (`teams.<time>.models`). Só as chaves que mudam entram no override; o resto continua igual. A
ordem é:
1. `teams.<time dono>.models`;
2. `repos[].models`;
3. `models` (o bloco global, com os padrões do projeto).

```yaml
teams:
  payments:
    models: { analysis: claude-opus-5-5, judge: claude-opus-5-5 }   # mais rigor neste domínio
repos:
  - name: acme/monorepo
    models: { triage: claude-sonnet-5-5 }                           # triagem com Sonnet
```

- **Time dono:** é o do roteamento. Para Triager e Analyst, é o dono do pacote; para Judge, linha
  de execução e intérprete de notas, é o dono do achado (no caso das notas, o achado que originou
  o PR). Architect e Curator usam o nível do repo.
- **Fallback de recusa:** `refusal_fallback` em `repos[].models` ou `teams.<time>.models` troca o
  modelo usado quando o do papel recusa, com a mesma ordem. No bloco global ele continua em
  `models.refusal.fallback_model`. Se o fallback for o próprio modelo que recusou, o item é pulado
  com o motivo.
- **Shepherd:** um override de `reviewer_code` sem `shepherd` leva o shepherd junto, como no bloco
  global.
- **Validação:** todo modelo de override precisa de preço em `pricing.models`. `refusal` e
  `use_batch` só existem no bloco global.
- **Estimativa** (`scan --estimate-only`): cada pacote é custeado com os modelos do time dono na
  triagem, na análise e no julgamento; o Architect e os achados dele usam o modelo do repo. Com
  modelos misturados, a coluna de modelo mostra todos (`claude-opus-5-5+claude-sonnet-5-5`).

Para ver o modelo final de cada papel e o nível que o decidiu:

```bash
./sentinela config models --repo acme/monorepo --team payments
```

### `llm`

| Campo | Padrão | Descrição |
|---|---|---|
| `provider` | `claude_code` | `claude_code` (assinatura), `anthropic_api` ou `replay` |
| `claude_code.binary` | `claude` | Binário do Claude Code |
| `claude_code.concurrency` | `2` | Chamadas em paralelo. O perfil qualidade escreve `3` (chamadas ao Opus mais longas) |
| `claude_code.max_calls_per_run` | `200` | Teto de chamadas da rodada |
| `claude_code.timeout` | `10m` | Por chamada |
| `claude_code.workdir` | `~/.cache/sentinela/claude-cwd` | Onde os agentes rodam; não pode ter `CLAUDE.md` acima |
| `claude_code.record_dir` | — | Grava as respostas para o provider `replay` |
| `replay.dir` | — | Respostas gravadas |

### `pricing`

Preço em USD por milhão de tokens, por modelo (`input`, `output`, `cache_read` e `tiers` por tamanho
de prompt), mais `batch_discount` e os multiplicadores de escrita no cache. Serve para estimar e
registrar o custo. Com `claude_code`, o custo registrado é o equivalente em preço de lista.

### `blocked_paths`

Globs que o Policy Gate nunca auto-aprova, em qualquer repo: pagamento, migrations, autenticação,
CI. O builder também é instruído a não tocar neles.

### `discovery`

| Campo | Padrão | Descrição |
|---|---|---|
| `workdir` | `.sentinela/repos` | Onde os repos são clonados |
| `scanners` | nenhum (o `init` e o `config bootstrap` geram pelas linguagens) | `golangci-lint`, `gosec`, `govulncheck`, `semgrep`, `osv-scanner` (vulnerabilidades em lockfiles e manifestos fora de Go: um achado por arquivo, ADR 0058), `eslint` (config neutra do Sentinela, sem a do repo nem as dependências dele) e `tsc` (`--noEmit`, só onde há `node_modules`; na descoberta, pulado), os dois das ferramentas de `node_tools` (ADR 0060), `go-test` (este executa código do repo). Os de uma linguagem rodam uma vez por unidade de build dela: módulos Go (`go.mod`), Gradle (`settings.gradle(.kts)` com `include`, `build.gradle(.kts)`) e Maven (`pom.xml`), e pacotes npm, yarn e pnpm (`package.json`, com `workspaces` ou `pnpm-workspace.yaml`). As unidades saem no resumo do scan, lidas só dos manifestos, sem rodar o build |
| `semgrep_config` | `p/default` | Pacote de regras do semgrep |
| `semgrep_languages` | `{java: [p/java], kotlin: [p/kotlin], typescript: [p/typescript, p/react, p/nodejs, p/expressjs], javascript: [p/javascript, p/nodejs, p/expressjs]}` | Pacotes de regras somados ao `semgrep_config` para cada linguagem presente no repo (um `--config` por pacote, sem repetir). `p/expressjs` é o pacote oficial do Express; o registro não tem um de Nest com regras (o `p/nestjs` está vazio). Um mapa no arquivo substitui o padrão inteiro; `{}` desliga. O resumo do scan mostra as regras usadas |
| `node_tools` | `tools/node` | Diretório das ferramentas de Node do Sentinela (eslint, typescript-eslint e typescript em versões fixas), instalado com `npm ci --ignore-scripts --prefix tools/node`. Sem ele, `eslint` e `tsc` aparecem pulados com o comando |
| `scanner_timeout` | `5m` | Por scanner |
| `triage_context_tokens`, `analysis_context_tokens` | `90000`, `150000` | Teto de contexto por chamada. Com o Triager em Haiku, o dele fica abaixo de 100000 (faixa de preço do Haiku). O perfil qualidade, com Opus, escreve `150000` e `300000` |
| `max_findings_per_package` | `5` | Limite de achados por pacote |
| `min_confidence` | `0.6` | Limiar do Judge para publicar |
| `similarity_threshold` | `0.92` | Similaridade acima disso é o mesmo achado |
| `location_candidates` | `5` | Teto de achados existentes no mesmo lugar do código (arquivo, símbolo ou linhas próximas, mesmo módulo) mostrados ao Judge; `0` desliga (ADR 0025) |
| `location_line_window` | `10` | Distância máxima, em linhas, para evidências no mesmo arquivo contarem como linhas próximas |
| `premise_margin` | — | Sem efeito desde a ADR 0054: achado que depende de decisão de produto sai sempre como pergunta (`labels.question`), sem passar pelo limiar e nunca como descarte. Aceito para não quebrar configs antigas |
| `revote_band` | `0.10` | Faixa em volta do limiar em que o Judge vota de novo |
| `judge_votes` | `1` | Votos na faixa (`3` liga; vale a mediana). O perfil qualidade escreve `3` |
| `batch_poll_interval` | `30s` | Intervalo de consulta da Batch API |
| `max_new_repos_per_run` | `1` | `scan --all`: repos nunca analisados por rodada, na ordem do arquivo; `0` só roda os já analisados (ADR 0035) |

### `execution` (Fase 4)

| Campo | Padrão | Descrição |
|---|---|---|
| `concurrency` | `1` | Jobs em paralelo, cada um no seu worktree. Jobs com arquivos em comum rodam em sequência (ADR 0031) |
| `worktrees`, `jobs` | `~/.cache/sentinela/<chave>/worktrees` e `.../jobs` | Fora de qualquer projeto, num diretório desta instalação (a chave é a do diretório da instalação, a mesma do `COMPOSE_PROJECT_NAME`; ADR 0056). Numa instalação com execuções nos diretórios compartilhados de antes (`~/.cache/sentinela/worktrees`), os que não estão no arquivo continuam neles |
| `plugin` | `plugins/sentinela-factory` | Plugin do Claude Code |
| `idle_timeout` | `15m` | Encerra a sessão sem saída nova; falha `idle_timeout`, retomável (ADR 0034). Negativo desliga |
| `completion_timeout` | `2m` | Encerra, com aviso, o processo que não sai depois do resultado; o resultado vale (ADR 0034). Negativo desliga |
| `binary` | o de `llm.claude_code` | Binário do Claude Code |
| `home_files` | — | Arquivos do HOME da máquina copiados para o HOME do job antes de cada job, relativos a ele (ex.: `[.npmrc, .gradle/gradle.properties, .m2/settings.xml]`), para builds com registro privado. Ficam legíveis pelo job: só com token de leitura (ADR 0057). A config recusa credenciais da máquina (`.ssh`, `.gitconfig`, `.config/gh`, `.claude` e afins) |
| `home` | `~/.cache/sentinela/<chave>/job-home` | HOME do Claude Code do job, só com o login dele: sem o git, o ssh e o gh da máquina (ADR 0049). `off` usa o HOME do worker |
| `runtime` | `host` | Onde o Claude Code do job roda: `host` (processo do worker) ou `container` (um contêiner por sessão, com montagens mínimas e saída de rede só pela lista permitida; ADR 0053) |
| `job_docker` | `none` | Docker do job no runtime container: `none` (nenhum; o job avisa quando o `AGENTS.md` pede Docker) ou `dind` (Docker isolado e privilegiado só para o job, derrubado no fim) |
| `container.engine` | `docker` | `docker` ou `podman` |
| `container.image` | `sentinela-job:latest` | Imagem do job (`Dockerfile.worker`) |
| `container.egress_allow` | Anthropic, GitHub, Go, npm, Maven, Gradle, PyPI e Docker Hub | Únicos destinos de rede do job: host exato, ou `.dominio` para os subdomínios. A lista substitui a padrão; para acrescentar o registro da empresa, repita a padrão (`config.DefaultEgressAllow`) com ele |
| `container.dind_image` | `docker:27-dind` | Imagem do Docker isolado (`job_docker: dind`) |
| `container.labels` | — | Labels a mais em cada contêiner, volume e rede que o worker cria (sempre há `sentinela.job=<nome>`) |
| `timeout` | `60m` | Por job |
| `poll_interval` | `1m` | Fila e acompanhamento dos PRs |
| `max_budget_usd` | `budget.per_pr_usd` | Teto da sessão (`--max-budget-usd`), em preço de lista |
| `shepherd_max_cycles` | `3` | Ciclos do PR Shepherd por PR |
| `max_resumes` | `2` | Retomadas de sessão seguidas no retry; ao passar, a tentativa falha e o retry seguinte recomeça do zero; negativo desliga (ADR 0030) |
| `review_notes` | `shadow` | `off`, `shadow` ou `publish`: notas fora do escopo dos reviewers viram achados candidatos |
| `disallowed_tools` | push, gh, curl, wget, WebFetch, WebSearch | Negados ao Claude Code no worktree |
| `ignore_comment_prefixes` | — | Começos de comentário que não viram instrução da equipe (além de `**Sentinela`): outra automação que comenta pela conta de uma pessoa (ADR 0026) |
| `failed_worktree_retention` | `168h` | Quanto o worktree de um job que falhou fica guardado; o de PR mergeado ou fechado sai no ciclo seguinte; negativo nunca remove (ADR 0037) |
| `jobs_retention` | `2160h` | Quanto o diretório de artefatos de um job (`jobs/<id>`, com o `stream.jsonl`) fica guardado depois do desfecho (PR mergeado ou fechado, falha ou cancelamento); nunca com o achado na fila ou rodando. Negativo nunca remove ([seguranca.md](seguranca.md#retenção-do-que-fica-gravado)) |
| `webhook_events_retention` | `720h` | Quanto o corpo de um webhook processado fica guardado; depois o payload é esvaziado e a linha fica para o dedup. Negativo nunca esvazia |

### `publish`

| Campo | Padrão | Descrição |
|---|---|---|
| `enabled` | `false` | Sem isso, modo sombra: nada sai do Postgres |
| `max_issues_per_team_per_run` | `3` | O excedente fica para a próxima rodada. No `scan --all`, conta por time na execução inteira, somando os repos, e publica no fim, por severidade (ADR 0050) |

### `feedback`

| Campo | Padrão | Descrição |
|---|---|---|
| `curator_interval` | `168h` | Frequência do Feedback Curator |
| `suppression_ttl` | `2160h` | Validade das supressões por padrão do Curator |
| `max_examples` | `6` | Exemplos de decisões humanas que o Judge vê |

### `trigger`

| Campo | Padrão | Descrição |
|---|---|---|
| `mode` | `polling` | `polling` ou `webhook` |
| `polling_interval` | `2m` | Intervalo do sync |
| `listen_addr` | `127.0.0.1:8080` | Endereço dos webhooks |

### `telemetry` e `signals`

- `telemetry.otlp_endpoint` liga o OpenTelemetry (traces por rodada, por agente e por chamada);
  vazio desliga.
- `signals.sentry` e `signals.datadog` são da Fase 5, ainda não implementada.

### `config.d/teams/<time>.yaml`

Um arquivo por time, ao lado do `sentinela.yaml`, juntado a ele ao carregar (ADR 0041). O nome do
arquivo é o nome do time. Serve para cada time manter, por PR, o que é dele.

| Chave | Descrição |
|---|---|
| `github`, `members`, `assignee`, `lead` | Como em `teams.<nome>` |
| `linear` | `team`, `project`, `triage_state`, `review_state`, `done_state`, `labels` e `category_projects` |
| `notify` | `slack` (canal) e `mention` (ID do grupo `S...`) |
| `labels` | `trigger`, `done`, `failed` |
| `policy` | Só `blocked`, `max_lines` e `max_files`: o time pode apertar, não liberar |
| `modules[]` | `repo` (declarado no principal), `path`, `linear_project` e `override`: módulos do time num repo compartilhado |
| `repos[]` | `name`, `default_branch`, `languages`, `linear_project`: repos inteiros do time |
| `blocked_paths` | Só acrescenta à lista da organização |

**Política da organização só vale no principal:** `policy.auto_approve`, `models`, `budget`,
`pricing`, `llm`, `adapters`, `github_apps` (inclusive `reviewer_approves`), `execution`,
`discovery`, `defaults` e os outros times. O `config validate` recusa a chave e diz em que arquivo
ela está e por quê. No principal, `teams.<time>` de um time de `config.d` pode ter só `policy` e
`models`, a parte da organização, que vence a do arquivo. O mesmo caminho de módulo em dois times
é erro.

`serve`, `worker` e `scan --all` releem o principal e o `config.d` a cada ciclo. Se a config
ficar inválida, eles continuam com a última válida e avisam no canal padrão. Adapters, banco e
credenciais só mudam no reinício. Para conferir um PR: `sentinela config validate --offline
--changed config.d/teams/<time>.yaml --base origin/main`.

## Variáveis de ambiente

| Variável | Uso |
|---|---|
| `DATABASE_URL` | Postgres |
| `GITHUB_TOKEN` | Token de leitura |
| `LINEAR_OAUTH_CLIENT_ID`, `LINEAR_OAUTH_CLIENT_SECRET`, `LINEAR_WEBHOOK_SECRET` | Linear (app OAuth do Sentinela, ADR 0032) |
| `LINEAR_API_KEY` | Linear, opcional: só com `linear_auth.mode: api_key` ou `fallback_api_key: true` |
| `SLACK_BOT_TOKEN` | Slack |
| `GITHUB_BOT_APP_ID`, `GITHUB_BOT_INSTALLATION_ID`, `GITHUB_BOT_PRIVATE_KEY_PATH` | App bot |
| `GITHUB_REVIEWER_APP_ID`, `GITHUB_REVIEWER_INSTALLATION_ID`, `GITHUB_REVIEWER_PRIVATE_KEY_PATH` | App revisora |
| `GITHUB_WEBHOOK_SECRET` | Webhook do GitHub |
| `ANTHROPIC_API_KEY`, `ANTHROPIC_WORKSPACE_ID` | Só com `llm.provider: anthropic_api` |
| `LINEAR_API_URL`, `GITHUB_API_URL`, `SLACK_API_URL`, `SENTINELA_GIT_BASE_URL` | Overrides para testes e proxies |
| `SENTINELA_TEST_DATABASE_URL` | Banco descartável dos testes de integração |
