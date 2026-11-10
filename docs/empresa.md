# Requisitos para uso em empresa

O que uma empresa precisa decidir e instalar para usar o Sentinela com vários times: provider e
custo, onde rodar, identidade, permissões e o caminho de adoção. Os modos estão em
[modos.md](modos.md); a config, em [guia-configuracao.md](guia-configuracao.md).

## Provider: chave ou plano da empresa, não assinatura individual

- **Descoberta (`scan`):** use `llm.provider: anthropic_api` com a chave da empresa
  (`ANTHROPIC_API_KEY`, de um workspace do Console da empresa; `ANTHROPIC_WORKSPACE_ID` se a chave
  não for de um workspace).
  - Isso dá Batch API (metade do preço nas etapas que não são interativas), prompt caching e os
    tetos em dólar de `budget.per_run_usd` e `budget.per_pr_usd`.
  - A assinatura individual (`claude_code`) serve para avaliar e não para uso da empresa: a cota é
    de uma pessoa e não há teto em dólar.
- **Linha de execução (`worker`):** roda o Claude Code headless (`claude -p`) e não usa a
  `ANTHROPIC_API_KEY` do `.env`, que é tirada do ambiente do processo. Ela usa o login do Claude
  Code do usuário do serviço. Faça esse login com uma conta da empresa: um plano corporativo do
  Claude, ou o Console da empresa. Nunca use a conta pessoal de alguém.
- **Teto por job:** `execution.max_budget_usd` (padrão: `budget.per_pr_usd`), repassado ao Claude
  Code como `--max-budget-usd`.
- **Perfil de modelos:** o padrão é qualidade, com Opus na triagem, na análise e na arquitetura
  ([custos.md](custos.md#perfis-de-modelos)).
  - A descoberta sai cerca de 2,2× a do perfil economia; a linha de execução fica mais barata
    (builder em Sonnet).
  - Com cota ou orçamento limitados, gere a config com `--models economia`.
  - O perfil pode mudar por time: `teams.<time>.models` só com os papéis que mudam.

**Estimar o custo por time:**

1. Antes de rodar, `sentinela scan --repo R --estimate-only` mostra as chamadas e o custo
   esperado por agente, com os modelos da config (`sentinela config models` mostra o perfil).
2. Depois de algumas semanas, o `sentinela status` mostra o custo dos últimos 30 dias:
   - **descoberta, por repo:** com o monorepo, divida pelos módulos de cada time;
   - **correções, por time dono:** pelo roteamento de cada achado.
3. Some com o `per_month_usd` de cada time em [custos.md](custos.md), onde estão as medições de
   referência e como limitar (modelos por papel, votos do Judge, limite de issues por rodada).
4. Com `anthropic_api`, os valores do `status` são dólar cobrado. Com `claude_code`, são o
   equivalente que o Claude Code reporta.

## Onde rodar

Um servidor (ou VM) da empresa, com um usuário de serviço próprio. São três processos e um banco:

| Peça | Comando | No servidor (systemd) | Em contêiner |
|---|---|---|---|
| Sync com o Linear e curadoria | `sentinela serve` | `sentinela-serve.service`, `Restart=always` | Deployment ou serviço com 1 réplica, reinício automático |
| Linha de execução | `sentinela worker` | `sentinela-worker.service`, `Restart=always` | Idem; precisa de git, do Claude Code logado e do plugin |
| Descoberta semanal | `sentinela scan --all` | `sentinela-scan.timer` (segunda 07:00) | CronJob ou agendador equivalente |
| Postgres 16 + pgvector | `docker compose up -d` | o container do compose, `restart: unless-stopped` | Postgres gerenciado com a extensão `vector` |

- **Arquivos prontos:** os de systemd estão em `deploy/` ([operacao.md](operacao.md#rodando-sozinho)).
- **Imagens:**
  - O `Dockerfile` gera uma imagem mínima, só com o binário, para `migrate` e `serve`.
  - O `Dockerfile.worker` gera a imagem do **job**: git, Go, JDK 21, Node com corepack, os
    scanners, o Claude Code (versão fixada), o plugin e o proxy de saída, com usuário sem privilégio
    e sem `gh`.
- **Runtime do job: use `execution.runtime: container`** ([seguranca.md](seguranca.md#runtime-do-job-host-ou-contêiner),
  ADR 0053):
  - O worker roda no host, ou numa VM, com os segredos e com acesso ao Docker (de preferência
    rootless) ou ao podman.
  - Cada sessão do Claude Code roda num contêiner da imagem do job, sem os segredos e sem o Docker
    do host, com saída de rede só para a lista permitida (Anthropic, GitHub para leitura e os
    registros de pacote; acrescente o registro interno da empresa em
    `execution.container.egress_allow`).
  - Repos com Testcontainers precisam de `execution.job_docker: dind`, que sobe um Docker
    isolado por job (privilegiado, na rede interna do job). Sem ele, o job avisa que esses testes
    não rodam.
  - O `scan` (descoberta) não executa código do repo, só os scanners, e pode continuar no host.
- **Releitura:** os três releem a config a cada ciclo. Um PR que muda `config.d/teams` vale sem
  reiniciar; adapters e credenciais pedem reinício.
- **Backup do Postgres:** o banco guarda o histórico que calibra as próximas rodadas (achados,
  decisões, supressões, exemplos, execuções). Faça `pg_dump` diário (o comando está em
  [operacao.md](operacao.md)) ou use o backup do Postgres gerenciado. Teste a restauração.
- **Segredos:** ficam fora do git, sempre.
  - Variáveis de ambiente: no `.env` do serviço ou no gerenciador de segredos da plataforma.
  - Chaves privadas das GitHub Apps: um arquivo com permissão 0600 (`GITHUB_*_PRIVATE_KEY_PATH`).
  - Token do app do Linear: `linear_auth.token_file` (padrão `~/.config/sentinela/linear-token.json`,
    0600). Num contêiner, use um volume persistente, porque o refresh token gira a cada uso.

## Identidade: o que os admins instalam

| Onde | O quê | Permissões | Quem instala |
|---|---|---|---|
| Linear | App OAuth "Sentinela", autorizado com `actor=app` (`sentinela linear auth`; callback `http://localhost:8787/oauth/linear/callback`) | escopos `read` e `write` (`linear_auth.scopes`); `write` cobre issues, comentários, labels e estados | um admin do workspace |
| GitHub | App bot ([manifesto](manifests/github-app-bot.json)) | `contents: write`, `pull_requests: write`, `metadata`, `checks`, `statuses` e `actions` como leitura | um owner da organização, só nos repos analisados |
| GitHub | App revisora ([manifesto](manifests/github-app-reviewer.json)) | `pull_requests: write`; `contents`, `metadata`, `checks` e `statuses` como leitura; eventos de PR, review, check e push | idem |
| Slack | App "Sentinela" ([manifesto](manifests/slack-app-manifest.yaml)) | `chat:write`, `chat:write.public`, `channels:read`, `links:write` (opcional) | um admin do workspace; depois `/invite` nos canais privados |

O passo a passo de cada um está em [integracoes.md](integracoes.md). O `sentinela config validate`
confere tudo isso: os times, estados e projetos do Linear, a identidade do app, as apps do GitHub
com as permissões efetivas e o acesso a cada repo, e se o bot está em cada canal.

**Por que o modo MCP é só para avaliação ou piloto:**

- **Identidade:** tudo sai com a conta de uma pessoa (issues, comentários, avisos). O Sentinela é
  reconhecido só pelo prefixo `**Sentinela`.
- **Permissões:** a cota e o acesso são de uma pessoa.
- **Sem webhook:** as decisões chegam com até `poll_interval` de atraso.
- **Gatilho não conferido:** o MCP não mostra quem aplicou a label, então `fix_trigger` não vale
  para o gatilho ([ADR 0042](adr/0042-quem-pode-disparar-a-correcao.md)).

Para a empresa, use o modo completo.

## Permissões: o que é política da organização

| Política | Chave | Onde |
|---|---|---|
| Quem pode pedir a correção | `fix_trigger.allowed: team_members` (ou uma lista de usuários do Linear); por time, `teams.<time>.fix_trigger` | só no principal (ADR 0042) |
| Auto-aprovação | `defaults.policy.auto_approve` e `teams.<time>.policy.auto_approve` | só no principal |
| A revisora pode aprovar | `github_apps.reviewer_approves` (padrão `false`: só comenta) | só no principal |
| Caminhos que nunca são auto-aprovados | `blocked_paths` | organização; o time só acrescenta no arquivo dele |
| Domínio sensível | `teams.<time>.policy.blocked: true` | time ou organização |
| Modelos, custos, modo | `models`, `budget`, `pricing`, `llm`, `adapters` | só no principal |

O resto (board, projetos, canal, menção, módulos, labels do time) é do time, no
`config.d/teams/<time>.yaml` dele, aprovado por PR. O `config validate` recusa uma chave da
organização num arquivo de time, dizendo qual chave, em qual arquivo e por quê.

**Recomendações:**

- `fix_trigger: { allowed: team_members }` desde o primeiro dia;
- auto-aprovação desligada até haver histórico (`sentinela metrics` mostra quantos bloqueios dos
  reviewers se confirmam);
- `blocked_paths` cobrindo dinheiro, auth, migrations, CI e infra.

## Caminho de adoção

1. **Local, num repo** (uma pessoa, uma tarde): `sentinela init --mode local`
   ([avaliacao.md](avaliacao.md)).
   - Veja se os achados prestam e recuse o que não presta.
   - As supressões ficam no Postgres e já calibram as próximas rodadas.
2. **Piloto MCP com um ou dois times** (algumas semanas): `sentinela init --mode mcp`.
   - O time decide no Linear, sem apps.
   - Meça a taxa de recusa, o custo (`sentinela status`) e o tempo até o PR.
3. **Modo completo:** os admins instalam o app do Linear, as GitHub Apps e o app do Slack.
   - Os times entram um por vez, cada um com o seu `config.d/teams/<time>.yaml`, por PR
     ([CONTRIBUTING.md](../CONTRIBUTING.md)).
   - O histórico dos passos anteriores fica no mesmo Postgres
     ([modos.md](modos.md#migrar-entre-modos-sem-perder-o-histórico)).

## Dois cenários

**Empresa pequena: um time, um board, um canal.** Um arquivo só,
[`examples/empresa-pequena/sentinela.yaml`](../examples/empresa-pequena/sentinela.yaml):

- modo completo, com `anthropic_api`;
- o time `engenharia` no board ENG, com o canal `#engenharia` e o grupo `@engenharia`;
- `fix_trigger: team_members`;
- dois repos inteiros do time.

O teste `TestSmallCompanyExampleValidates` valida o arquivo com Linear e Slack falsos (0 erros,
0 avisos) e confere o roteamento.

```yaml
adapters: { tracker: linear_api, notifier: slack_api, git_host: github_apps }
defaults:
  linear: { team: ENG, project: "Sentinela", labels: [sentinela], triage_state: Triage }
  notify: { slack: C0EMPRESA01 }
fix_trigger: { allowed: team_members }
llm: { provider: anthropic_api }
teams:
  engenharia: { github: ["@empresa/engenharia"], linear: { team: ENG }, notify: { mention: S0EMPRESA01 } }
repos:
  - { name: empresa/api, team: engenharia }
  - { name: empresa/web, team: engenharia }
# mais labels, github_apps, budget, models, pricing, blocked_paths e publish: veja o arquivo
```

**Empresa com 10 times.** Use o formato do cenário
[`examples/monorepo-acme`](../examples/monorepo-acme):

- um `sentinela.yaml` principal, dos administradores, com a política da organização e os repos
  compartilhados;
- um `config.d/teams/<time>.yaml` por time, cada um dono do seu arquivo pelo CODEOWNERS do repo
  da config;
- o workflow que roda `config validate --offline --changed` em cada PR e comenta o roteamento antes
  e depois.

O cenário acme tem 4 times; 10 é o mesmo formato com mais arquivos. O
[guia de configuração](guia-configuracao.md) monta o cenário do zero.
