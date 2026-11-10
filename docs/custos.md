# Custos

O custo depende de onde os agentes rodam:

| `llm.provider` | Como é cobrado | Limite da rodada |
|---|---|---|
| `claude_code` | Pela assinatura do Claude: sem custo por token, mas consome o limite de uso | `llm.claude_code.max_calls_per_run` (chamadas) |
| `anthropic_api` | Por token, pela tabela de preços (`pricing`) | `budget.per_run_usd` (dólares) |
| `replay` | Nada: respostas gravadas | — |

Com `claude_code`, o Sentinela registra em `llm_calls` o custo equivalente em preço de lista quando
o Claude Code o informa. O número serve para comparar, não é cobrado. A Batch API (metade do preço)
só existe com `anthropic_api`; na assinatura, cada chamada é um `claude -p` e consome a cota.

## Perfis de modelos

O bloco `models` do `sentinela.yaml` é explícito, papel a papel. Dois perfis servem de ponto de
partida (ADR 0051):

- **qualidade (padrão):** modelo forte onde há julgamento que não se recupera depois. Um pacote
  que a triagem descarta nunca chega à análise.
- **economia:** para orçamento ou cota limitados.

| Papel | qualidade | economia |
|---|---|---|
| `triage`, `analysis`, `architect` | Opus | Haiku na triagem, Sonnet na análise e na arquitetura |
| `judge` | Sonnet, `judge_votes: 3` | Sonnet, `judge_votes: 1` |
| `spec_writer` | Sonnet | Sonnet |
| `executor` (builder) | Sonnet, sem escalada para Opus | Opus |
| `reviewer_security`, `approver` | Opus | Opus |
| `reviewer_code`, `reviewer_reliability`, `reviewer_domain` | Sonnet | Sonnet |
| `curator` | Sonnet | Haiku |
| `notes`, `orchestrator`, `shepherd` | Sonnet | Sonnet |
| `refusal.fallback_model` | Sonnet | Opus |

O perfil também escreve os ajustes que andam com os modelos:

| Chave | qualidade | economia |
|---|---|---|
| `discovery.triage_context_tokens` | 150000 | 90000 (abaixo de 100k: acima disso o Haiku muda de faixa de preço) |
| `discovery.analysis_context_tokens` | 300000 | 150000 |
| `llm.claude_code.concurrency` | 3 | 2 |
| `llm.claude_code.timeout` | 15m | 10m |
| `budget.per_run_usd` (só `anthropic_api`) | 10 | 5 |

Como escolher e conferir:

```bash
./sentinela init --mode local --models economia      # qualidade é o padrão; personalizado parte do qualidade
./sentinela config bootstrap --repo acme/svc --models economia   # personalizado mantém os da --config
./sentinela config models --repo acme/svc --team pagamentos      # modelo, onde foi definido e perfil
```

O `config models` mostra o perfil do bloco global (qualidade, economia ou personalizado) e, papel a
papel, a que perfil o modelo corresponde. Uma instalação com o bloco já escrito não muda de modelo
quando o padrão muda.

## Antes de rodar: estimativa

```bash
./sentinela scan --repo acme/monorepo --estimate-only
```

A estimativa mostra chamadas e custo esperado e no pior caso, por agente, sem chamar modelo nenhum.
- **Com `anthropic_api`:** a rodada não começa se o custo esperado passar de `budget.per_run_usd`.
  O pior caso é barrado lote a lote durante a rodada.
- **Com `claude_code`:** a rodada não começa se as chamadas esperadas passarem do limite.
- **Pior caso acima do teto** (`llm.claude_code.max_calls_per_run`, ou o teto em dólar com a API):
  a estimativa avisa. Diz qual agente pesa mais e o que fazer: subir o teto, diminuir
  `discovery.max_findings_per_package` (cada achado é uma chamada do Judge) ou rodar por partes.
  Se a rodada chegar ao teto, ela para no meio (`aborted_budget`), e o que faltou fica para a
  próxima.

## Medições de referência

**Estimativa da rodada completa por perfil** (`scan --full --estimate-only` com `anthropic_api`,
em batch; 2026-10-16, três repos reais):

| Repo | Pacotes | Chamadas | economia: esperado | qualidade: esperado | qualidade: pior caso |
|---|---|---|---|---|---|
| Go, ledger | 15 | 41 | US$ 0,42 | US$ 0,96 | US$ 5,23 |
| Go, encurtador de links | 14 | 37 | US$ 0,40 | US$ 0,93 | US$ 4,93 |
| Monorepo Go, Java, Kotlin e Python | 46 | 117 | US$ 1,16 | US$ 2,55 | US$ 15,13 |

- **Quanto custa a mais:** o qualidade sai cerca de 2,2× o economia nesses repos. A triagem
  (Opus em todos os pacotes) e a análise (Opus nos aprofundados) são a diferença. O Judge custa o
  mesmo; os votos extras perto do limiar não entram no esperado.
- **Rodada incremental:** analisa só os pacotes mudados desde o último `head_sha`. Custa uma
  fração disso, proporcional ao que mudou, e nada sem commits novos.
- **Pior caso:** fica acima do `budget.per_run_usd` no monorepo. O orçamento barra lote a lote, e
  os pacotes que sobram ficam para a rodada seguinte.

As medições abaixo são de antes dos perfis, com os modelos que hoje formam o perfil economia, em
dois repositórios de teste:

**Descoberta completa (`scan --full`)**

| Repo | Pacotes | Chamadas | Tempo (assinatura) | API: esperado | API: pior caso |
|---|---|---|---|---|---|
| App Java + Angular | 14 | 20 a 25 | ~1,5 min | US$ 0,40 | US$ 3,00 |
| Monorepo Go, Java, Kotlin e Python | 46 | 63 a 69 | ~3,3 min | US$ 1,16 | US$ 9,31 |

Os valores de API são da estimativa, com Batch API. O que mais pesa:
- **Analyst:** só roda nos pacotes que o Triager manda aprofundar (Sonnet no economia, Opus no
  qualidade).
- **Triager:** no economia, em Haiku, quase não pesa. No qualidade, em Opus, roda em todos os
  pacotes e passa a ser a maior parte do custo da rodada completa.
- **Judge (Sonnet):** uma chamada por achado proposto, e até 3 perto do limiar.

**Outras operações**

| Operação | Medição |
|---|---|
| Rodada incremental sem commits novos | Nenhuma chamada (`no_changes`) |
| Rodada incremental | Só os pacotes alterados |
| Job da linha de execução, correção pequena em Go | 1 rodada, 13 turnos, 91 s, US$ 0,67 em preço de lista (Opus US$ 0,41, Sonnet US$ 0,26) |
| Notas de review de um PR | 1 chamada do intérprete e 1 do Judge por achado candidato |
| `rejudge` de 40 achados | 40 chamadas do Judge, ~2 min |

O job medido acima é do perfil economia: Opus no builder, na revisão de segurança e no approver,
Sonnet no orquestrador e em três revisões. No qualidade, o builder passa a Sonnet e o job fica mais
barato, com Opus só na revisão de segurança e no approver. Um job com revisões pedindo mudança custa
mais, até 3 rodadas.

## Como limitar

- **Perfil economia:** `--models economia` no `init` ou no `config bootstrap`, ou o bloco da tabela
  acima. É a alavanca maior: cerca de metade do custo da descoberta.
- **Orçamento:** `budget.per_run_usd` (API) ou `llm.claude_code.max_calls_per_run` (assinatura).
- **Teto por job:** `execution.max_budget_usd`, passado ao Claude Code como `--max-budget-usd`.
- **Modelo por papel** em `models`, por exemplo Sonnet no builder. Para mudar só onde vale a
  pena, use `repos[].models` ou `teams.<time>.models` (por exemplo Opus na análise só do time de
  pagamentos). Use `sentinela metrics` para ver se a qualidade dos PRs se mantém.
- **Votação do Judge:** `discovery.judge_votes: 1` desliga os votos extras perto do limiar.
- **Menos achados por rodada:** `discovery.max_findings_per_package` e
  `publish.max_issues_per_team_per_run`.
- **Agendamento:** rodadas incrementais frequentes custam menos que rodadas completas raras.
- **Modo MCP:** cada leitura do Linear é uma chamada `claude -p`. Com
  `adapters.mcp.poll_interval: 15m`, são cerca de 192 por dia entre `serve` e `worker`,
  qualquer que seja o número de issues ([modos.md](modos.md#quanto-custa-o-polling-no-modo-mcp)).
- **Módulos ignorados:** `repos[].modules[].ignore: true` tira código gerado, vendorizado ou legado
  da análise.

## Onde ver o custo

`sentinela status` mostra o custo dos últimos 30 dias: a descoberta por repo e as correções por
time dono. Esse é o ponto de partida para estimar o custo por time numa empresa
([empresa.md](empresa.md#provider-chave-ou-plano-da-empresa-não-assinatura-individual)).


- `sentinela report --run N`: custo e chamadas por agente na rodada.
- `sentinela metrics`: custo médio e total dos jobs da linha de execução.
- Tabela `llm_calls`: cada chamada, com modelo, tokens, custo e versão do prompt.
