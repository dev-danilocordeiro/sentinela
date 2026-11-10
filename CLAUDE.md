# CLAUDE.md

Instruções permanentes do projeto, copiadas da seção "Prompt" de `SENTINELA_BUILD_PROMPT.md`.

Você vai construir o **Sentinela**, um sistema local de Autonomous Reliability Engineering. Ele analisa repositórios, encontra problemas (bug potencial, performance, melhoria), abre issues no Linear e, quando um humano aplica uma label, implementa a correção e abre um PR. Cada decisão humana volta para um Postgres que calibra as próximas rodadas. Numa fase posterior, sinais de produção do Sentry e do Datadog entram como novas fontes de achados.

### Princípios (não negocie)

1. **Agente só onde há julgamento.** Scanner, publicação no Linear, fila e política de aprovação são código Go determinístico e testado. LLM entra em triagem, análise, julgamento, execução, review e interpretação de sinais.
2. **Contratos tipados entre etapas.** Todo agente LLM recebe e devolve JSON validado contra schema (structured outputs via `output_config.format`, conforme ADR 0003). Nada de texto livre passando entre etapas.
3. **Modelos definidos por perfil.** O padrão é qualidade: modelo forte onde há julgamento que não se recupera depois (triagem, análise, arquitetura, revisão de segurança e aprovação). O perfil economia fica disponível para quem roda com orçamento ou cota limitados. Use Batch API em tudo que não é interativo (provider `anthropic_api`) e prompt caching no contexto comum.
4. **Tudo idempotente e auditável.** Cada chamada de modelo grava em `llm_calls` (agente, modelo, tokens, custo, latência, versão do prompt). Webhooks são deduplicados por `event_id`.
5. **Segurança por padrão.** Executor em git worktree isolado, PRs sempre em draft até o gate, nunca push na branch padrão, paths bloqueados configuráveis (pagamentos, cobrança, migrations, auth, infra).
6. **Decisão humana é do humano.** Nem o Sentinela nem um assistente que trabalhe no projeto executam ações que representam uma decisão humana em nome do usuário: cancelar ou fechar issue, aplicar a label `sentinela-fix`, aprovar ou mergear PR, revogar supressão. Quando o próximo passo for uma dessas ações, pare e peça a quem decide. O Sentinela só lê essas decisões (webhook ou polling) e reage a elas. Decidir e executar são coisas diferentes. A decisão é sempre humana (`decided_by = human`). A execução pode ser de um agente a pedido explícito de quem decidiu, que escolheu o que fazer; esse `outcome` é gravado com `executed_by = agent` e uma nota, e conta normalmente nas métricas, no Curator e nas supressões, porque a decisão foi humana. `decided_by = agent` (um agente escolhendo sozinho) viola este princípio: fica fora das métricas, do Curator e das supressões, e o `config validate` avisa.

### Stack

- Go 1.27+, binário único `sentinela` com subcomandos (`scan`, `serve`, `worker`, `migrate`, `report`). Use `cobra`.
- Postgres 16 + `pgvector`, migrations com `goose`, acesso com `pgx` + `sqlc`.
- SDK oficial da Anthropic para Go para as chamadas de modelo. Antes de codar, confira na documentação atual da Anthropic os IDs de modelo, structured outputs, Batch API, prompt caching e o MCP connector da Messages API.
- Linha de execução como **plugin do Claude Code** (`plugins/sentinela-factory/`): uma skill orquestradora (`/sentinela-fix <issue>`) e um subagente por papel (spec-writer, builder, os quatro reviewers, approver). Inspirado em https://github.com/zazencodes/zazencodes-season-3/tree/main/src/software-factory-claude-code, adaptado para backend, PRs e Postgres. O worker invoca em modo headless (`claude -p "/sentinela-fix EX-123"`, saída `stream-json`), com o plugin carregado, sem copiar `.claude/` para os repos-alvo.
- Linear via API GraphQL; GitHub via duas GitHub Apps (`sentinela-bot` abre PRs, `sentinela-reviewer` aprova).
- `docker compose` com Postgres e o app; config em `sentinela.yaml` + variáveis de ambiente para segredos.
- Observabilidade do próprio Sentinela com OpenTelemetry (traces por rodada e por execução).

### Estrutura do repo

```
cmd/sentinela/            # main + subcomandos cobra
internal/config/          # sentinela.yaml, repos, paths bloqueados, orçamentos
internal/store/           # sqlc, migrations, fila (FOR UPDATE SKIP LOCKED)
internal/scan/            # adaptadores de scanners (golangci-lint, gosec, govulncheck, semgrep, testes)
internal/repo/            # clone, diff desde último SHA, mapa de pacotes, CODEOWNERS, grafo de deps
internal/llm/             # cliente Anthropic, batch, cache, contabilidade de custo, schemas
internal/agents/          # um pacote por agente (ver roster)
internal/signals/         # interface SignalSource + adaptadores (code, sentry, datadog)
internal/linear/          # publisher de issues, sync de estado via webhook/polling
internal/github/          # apps, PRs, checks, webhooks
internal/executor/        # worktree, invocação do Claude Code, coleta de resultado
internal/gate/            # política de auto-aprovação
internal/feedback/        # supressões, few-shot, calibração de limiares
prompts/                  # prompts versionados (um arquivo por agente, com versão no frontmatter)
schemas/                  # JSON Schemas dos contratos entre etapas
```

### Roster de agentes

| Agente | Tipo | Modelo | Entrada → Saída |
|---|---|---|---|
| Scanner | código | — | repo + diff → `ScanSignal[]` |
| Package Triager | LLM, 1 por pacote | Opus, batch (economia: Haiku) | código do pacote + interfaces vizinhas + sinais do scanner + supressões → `TriageResult` (categoria, aprofundar sim/não, motivo) |
| Package Analyst | LLM | Opus, batch (economia: Sonnet) | pacote sinalizado + contexto → `Finding[]` com evidência arquivo:linha |
| System Architect | LLM, 1 por rodada | Opus, batch (economia: Sonnet) | grafo de dependências + resumos dos pacotes + achados → `Finding[]` entre módulos |
| Judge | LLM | Sonnet, batch | `Finding` + similares no pgvector + exemplos aceitos/recusados → `Verdict` (publicar, descartar, mesclar; confiança 0–1) |
| Linear Publisher | código | — | `Finding` aprovado → issue no Linear (projeto do repo, dono via CODEOWNERS, labels, fingerprint) |
| Spec Writer | subagente Claude Code | Sonnet | issue do Linear → `spec.md` com contexto, requisitos, critérios de aceite testáveis, fora de escopo e premissas (pula se a issue já veio do Judge com critérios) |
| Builder | subagente Claude Code | Sonnet (economia: Opus) | `spec.md` → commits na branch do job + `build.md` (primeira linha `STATUS: READY` ou `STATUS: BLOCKED`, com evidência de cada critério) |
| Reviewer Security | subagente Claude Code | Opus | diff + spec → `review-security.md` (primeira linha `VERDICT: PASS` ou `VERDICT: CHANGES`) |
| Reviewer Code | subagente Claude Code | Sonnet | diff + spec → `review-code.md`: correção, simplicidade, convenções do repo |
| Reviewer Reliability | subagente Claude Code | Sonnet | diff + spec → `review-reliability.md`: concorrência, erros, timeouts, idempotência, performance |
| Reviewer Domain Risk | subagente Claude Code | Sonnet | diff + spec + roteamento → `review-domain.md`: dinheiro, dados pessoais (LGPD), contratos de API, regras do time |
| Approver | subagente Claude Code | Opus | tudo acima → `decision.md` (primeira linha `APPROVE` ou `ESCALATE`, com motivo e o que o humano precisa checar) |
| Policy Gate | código | — | `decision.md` + regras do time + CI → aprova o PR pela GitHub App revisora ou pede CODEOWNERS |
| PR Shepherd | Claude Code headless | Sonnet | PR com CI vermelho ou review pedindo mudança → novo commit (máx. 3 ciclos) |
| Feedback Curator | LLM | Sonnet (economia: Haiku) | desfechos da semana + motivos de recusa → supressões e exemplos few-shot atualizados |
| Sentry Interpreter | LLM + MCP (Fase 5) | Sonnet | issue do Sentry → `Finding` com stack, release, frequência, usuários afetados |
| Datadog Interpreter | LLM + MCP (Fase 5) | Sonnet | monitor/APM/log anômalo → `Finding` com serviço, endpoint, métrica e janela |

Cada agente LLM fica em `internal/agents/<nome>/` com: prompt em `prompts/<nome>.md` (versionado), schema de entrada e saída em `schemas/`, função `Run(ctx, in) (out, error)` e testes com respostas gravadas (golden files), sem chamar a API nos testes.

### Contrato central: `Finding`

Todas as fontes (código, Sentry, Datadog) produzem o mesmo `Finding`. É isso que permite plugar fontes novas sem mexer no juiz, no publisher nem no executor.

```json
{
  "source": "code_analysis | sentry | datadog | review_note",
  "repo": "string", "module": "string", "owners": ["@team"],
  "routing": {"team": "payments", "linear_team": "PAY", "linear_project": "", "assignee": "", "rule": "config:services/billing/** | codeowners:L12 | repo | fallback"},
  "category": "bug | performance | improvement | security",
  "severity": "critical | high | medium | low",
  "confidence": 0.0,
  "title": "imperativo, curto",
  "context": "por que importa",
  "evidence": [{"file": "", "line": 0, "permalink": "", "snippet": ""}],
  "signals": [{"kind": "scanner | sentry_event | dd_monitor | dd_trace", "ref": "", "summary": ""}],
  "proposed_fix": "o que mudar e onde",
  "acceptance_criteria": ["verificável"],
  "scope_paths": ["dirs que podem ser alterados"],
  "fingerprint": "sha1(repo|category|file|problema-normalizado)[:12]"
}
```

### Interface de fontes de sinal

```go
type SignalSource interface {
    Name() string                                    // "code_analysis", "sentry", "datadog"
    Collect(ctx context.Context, since time.Time) ([]RawSignal, error)
    Interpret(ctx context.Context, s RawSignal) ([]Finding, error) // agente especialista da fonte
}
```

A fonte `code_analysis` é a pipeline Scanner → Triager → Analyst → Architect. Sentry e Datadog implementam a mesma interface na Fase 5.

### Roteamento de ownership: repos, módulos, times e boards

O Sentinela precisa funcionar tanto com um repo por time quanto com um monorepo onde cada módulo pertence a um time diferente, com board próprio no Linear. Isso é configuração, não código: quem adotar o projeto edita só o `sentinela.yaml`.

```yaml
# sentinela.yaml (trecho de ownership)
defaults:
  linear:
    team: PLAT                 # time do Linear usado quando nada mais casar
    labels: [sentinela]
    triage_state: Triage
  owners_source: both          # codeowners | config | both (config tem precedência)
  notify: { slack: C0123ABCDEF }  # canal padrão: resumo de cada rodada e times sem canal próprio
  policy:
    auto_approve: false

teams:                         # um registro por time de engenharia
  payments:
    github: ["@acme/payments"]          # como o time aparece no CODEOWNERS
    linear:
      team: PAY                          # board próprio (time do Linear)...
      project: "Sentinela · Payments"    # ...ou só um projeto num time compartilhado
      triage_state: Triage
    assignee: none                       # none | lead | round_robin
    lead: maria@empresa.com
    notify: { slack: C0PAYALERT1, mention: S0PAYTEAM1 }  # ID do canal (ou "#payments-alerts") e user group a mencionar
    policy: { auto_approve: false, blocked: true }   # domínio sensível: nunca auto-aprova
  platform:
    github: ["@acme/platform", "@acme/sre"]
    linear: { team: PLAT, project: "Sentinela · Platform" }
    assignee: round_robin
    members: [joao@empresa.com, ana@empresa.com]
    policy: { auto_approve: true, max_lines: 50 }

repos:
  - name: acme/monorepo
    default_branch: main
    modules:                             # opcional; sobrescreve o CODEOWNERS
      - path: "services/billing/**"
        team: payments
      - path: "libs/**"
        team: platform
      - path: "services/legacy/**"
        ignore: true                     # fora da análise
      - path: "**/Dockerfile"            # glob por tipo de arquivo: imagem e runtime são da
        team: platform                   # plataforma em qualquer módulo (o CODEOWNERS daria o
                                         # Dockerfile de services/billing ao payments)
    fallback_team: platform              # caminho sem dono
  - name: acme/app-java-angular                  # repo simples: um time só
    team: platform
```

**Regras de resolução (implemente exatamente nesta ordem e cubra com testes de tabela):**

1. `modules[].path` da config, vencendo o glob mais específico (mais longo).
2. `CODEOWNERS` do repo com a semântica do GitHub: a última regra que casa vence. O dono vira time via `teams[].github`; usuário individual vira o time de que ele é membro, se houver.
3. `repos[].team` do repo.
4. `repos[].fallback_team`, depois `defaults`.

Com `owners_source: codeowners` pule o passo 1; com `config`, pule o 2. Quando a regra casar com vários donos, o primeiro é o dono principal e os demais entram como inscritos na issue. Owner do CODEOWNERS sem time mapeado gera aviso no `config validate`, nunca falha silenciosa.

**Dono de um achado:** o dono é o do arquivo da evidência principal, exceto quando ele sai de uma regra genérica (`*` do CODEOWNERS, `repos[].team`, `fallback_team` ou `defaults`). Nesse caso, se algum caminho do achado casa com uma regra mais específica, vence o caminho mais específico, nesta prioridade: `scope_paths`, depois as demais evidências, depois os pacotes que o código da evidência ou a correção citam (em Go, `turmas.ErrX` resolvido pelos imports do arquivo). A regra gravada mostra de onde veio, por exemplo `codeowners:L11 via scope:go/internal/turmas`. Um handler em `go/cmd/api` (curinga, plataforma) que valida regra de turmas com escopo em `go/internal/turmas` vai para o acadêmico. Veja a ADR 0017.

**Achados que cruzam módulos** (vindos do System Architect): o dono é o módulo do arquivo da evidência principal. Para cada outro módulo afetado, crie uma issue relacionada no board do time dele, linkada à principal.

**O que o roteamento decide:** board e projeto no Linear, estado inicial, assignee, canal de aviso, política do gate (auto-aprovação, limites, bloqueio) e os reviewers do PR. A resolução vai gravada no achado (`routing` no `Finding`), para que mudanças futuras no CODEOWNERS não reescrevam o passado.

**Avisos no Slack:** um único bot posta em vários canais.

- O resumo de cada rodada vai para `defaults.notify.slack`.
- Cada issue criada (e, na Fase 4, cada PR) vai para `teams.<time>.notify.slack` do time dono resolvido pelo roteamento; issues relacionadas vão para o canal do time delas. Time sem canal cai no canal padrão.
- O canal aceita ID (`C...`, preferível) ou nome (`#canal`); nome é resolvido para ID uma vez e fica em cache.
- `notify.mention` é opcional por time: ID de user group (`S...`), de usuário (`U...`) ou `@here`. Sem ele, ninguém é mencionado. Handle (`@grupo`) sai como texto, porque o Slack só notifica grupos por ID.
- Erro de postagem (`not_in_channel`, `channel_not_found`) nunca derruba a rodada: vira aviso, e o canal padrão recebe uma mensagem dizendo qual canal falhou e por quê.

**Comandos de apoio:**

- `sentinela config validate`: checa times do Linear, projetos, estados e times do GitHub que existem de verdade, lista owners do CODEOWNERS sem time mapeado e confere cada canal do Slack configurado (existe, não está arquivado e o bot é membro, ou seja, consegue postar).
- `sentinela owners explain <repo> <path>`: mostra qual regra decidiu o dono de um caminho e por quê.
- `sentinela config bootstrap --repo <repo>`: lê o CODEOWNERS e os times do Linear e gera um `sentinela.yaml` inicial para revisão humana.

### Esquema do banco (mínimo)

`analysis_runs`, `findings` (com `embedding vector(1024)`, `state`, `resolution_reason`), `suppressions`, `executions` (fila), `webhook_events`, `outcomes`, `llm_calls`, `prompt_versions`. Os estados de `findings` são `open | rejected | executing | fixed | reverted | stale`. Toda migration tem `down`.

### Fases e critérios de aceite

**Fase 1: Fundação**
- `docker compose up` sobe Postgres com pgvector; `sentinela migrate up` e `down` funcionam.
- `sentinela.yaml` lista repos, módulos, times, paths bloqueados, orçamento por rodada e por PR, no formato da seção "Roteamento de ownership".
- O resolvedor de ownership passa em testes de tabela cobrindo: repo de um time, monorepo com config, monorepo só com CODEOWNERS, regra com vários donos, caminho sem dono e owner sem time mapeado.
- `sentinela config validate`, `sentinela owners explain` e `sentinela config bootstrap` funcionam contra Linear e GitHub mockados.
- `internal/llm` faz chamada simples e em batch, grava em `llm_calls` com custo calculado pela tabela de preços configurável, e respeita orçamento: aborta a rodada ao estourar.
- CI local: `go test ./...` e `golangci-lint run` passam.

**Fase 2: Descoberta em modo sombra**
- `sentinela scan --repo X` clona, calcula o diff desde o último `head_sha` (ou repo inteiro na primeira vez), roda os scanners disponíveis e mapeia pacotes e CODEOWNERS.
- Triager, Analyst, Architect e Judge rodam via Batch API e gravam `findings`, sem publicar nada.
- O contexto de cada chamada ao Triager respeita `discovery.triage_context_tokens`; com o Triager em Haiku (perfil economia), fica abaixo de 100k tokens (acima disso o preço do Haiku muda de faixa); teste cobrindo o corte.
- `sentinela report --run <id>` gera um HTML local com achados, custo por agente e decisões do juiz.
- Rodar duas vezes seguidas sem commits novos não gera achados novos (dedup por fingerprint e similaridade).
- Recusa do modelo: fallback configurável (outro modelo ou pular o pacote com motivo gravado), sem derrubar a rodada.
- OpenTelemetry: um trace por rodada, com spans por repo, por agente e por chamada de modelo (tokens e custo como atributos); exporter OTLP configurável e desligado por padrão.

**Fase 3: Linear e feedback**
- Publisher cria cada issue no board e projeto do time resolvido pelo roteamento, com estado inicial, assignee e aviso do time, limite por time por rodada, labels e fingerprint no corpo.
- Achado entre módulos gera a issue principal no time dono e issues relacionadas, linkadas, nos boards dos outros times.
- `sentinela serve` recebe webhook do Linear (assinatura validada, dedup por `event_id`); modo polling alternativo configurável.
- Issue cancelada vira `rejected` e gera supressão; o Feedback Curator roda semanalmente e atualiza few-shot e limiares por categoria e módulo.
- Teste de ponta a ponta com Linear mockado: achado recusado não reaparece na rodada seguinte.

**Fase 4: Execução, gate e shepherd**
- Label `sentinela-fix` enfileira uma execução; `sentinela worker` consome com `SKIP LOCKED` e concorrência configurável.
- O worker cria um git worktree por job (branch `sentinela/<issue-id>`) e roda o plugin `sentinela-factory` em modo headless. Jobs diferentes rodam em paralelo, cada um no seu worktree.
- A linha segue o loop: spec-writer → builder → os quatro reviewers **em paralelo** → espera todos. Se algum devolver `VERDICT: CHANGES`, o **mesmo** builder é retomado com os arquivos de review (no máximo 2 retomadas, 3 rodadas). Com tudo `PASS`, roda o approver. Rodadas esgotadas viram `ESCALATE` automático.
- Contratos rígidos: cada agente escreve exatamente um arquivo, a primeira linha é o veredito, e só a skill orquestradora escreve o estado do job. Arquivo faltando ou primeira linha inválida para o job e marca `failed`.
- Os agentes não carregam nada específico de repo: comandos de setup, teste, build e lint vêm do `AGENTS.md` ou `CLAUDE.md` do repo-alvo. Se o repo não tiver, o job falha com uma mensagem dizendo o que falta.
- Ao final, o worker abre o PR em draft com `Fixes <ID>`, anexa `spec.md`, `build.md`, os reviews e `decision.md` ao corpo do PR (resumidos) e grava tudo em `executions`, com um registro por review (`reviews`: execução, rodada, lente, veredito, achados).
- Policy Gate (código) só aprova o PR pela GitHub App revisora com `decision.md` = `APPROVE` **e** a política do time resolvido (tamanho, arquivos, categoria, paths bloqueados, CI verde, histórico de revert do módulo). Time com `blocked: true` nunca é auto-aprovado. A lista do approver para `ESCALATE` inclui: dinheiro, auth/permissões, CI/deploy, migrations, dependência nova, dados pessoais e premissa da spec que seja decisão de produto.
- Comandos de humano: `sentinela job approve <id>` (aprova um job em `ESCALATE`) e `sentinela job rework <id> "<nota>"` (devolve ao builder com a nota, com rodadas zeradas).
- Métricas por lente gravadas: quantas vezes cada reviewer bloqueia, quantos bloqueios se confirmam no review humano, custo e rodadas por job. Por padrão aprova sem auto-merge; auto-merge é flag por categoria.
- PR Shepherd reage a CI vermelho e a review pedindo mudança, com no máximo 3 ciclos.
- Webhook do GitHub grava `outcomes` (merged, closed, reverted) e retroalimenta `findings`.

**Fase 5: Sentry e Datadog**
- Implementar `SignalSource` para Sentry e Datadog. O agente especialista de cada fonte usa o MCP server oficial da ferramenta (pelo MCP connector da Messages API ou um cliente MCP em Go) para buscar detalhes: stack trace, release, tags e frequência no Sentry; monitores, traces APM, logs e métricas no Datadog.
- Cada intérprete mapeia o sinal para o repo e módulo certos (via tags de serviço → `sentinela.yaml`) e produz `Finding` com `source` correspondente, passando pelo mesmo Judge.
- Correlação: se um achado de código e um sinal de produção apontam para o mesmo arquivo ou função, o Judge mescla e aumenta a severidade.
- Se o MCP estiver indisponível, cair para a API REST da ferramenta com o mesmo contrato.

### Regras de trabalho para você (Claude)

- Comece lendo este arquivo inteiro e criando `docs/adr/` com uma ADR curta por decisão relevante que você tomar.
- Trabalhe uma fase por vez; ao terminar, rode os testes e liste o que ficou fora.
- Não invente IDs de modelo, endpoints ou flags: confira na documentação atual e cite a fonte num comentário.
- Segredos só via variável de ambiente; nunca commitados.
- Pergunte antes de qualquer decisão que mude o contrato `Finding` ou o esquema do banco depois da Fase 2.
