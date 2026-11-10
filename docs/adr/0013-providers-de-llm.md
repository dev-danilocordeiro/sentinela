# ADR 0013: Providers de LLM configuráveis (anthropic_api, claude_code, replay)

- Status: aceita
- Data: 2026-10-10

## Contexto
Nos testes pessoais não deve haver gasto com a API da Anthropic. O dono do repo tem assinatura do
Claude, que o Claude Code CLI usa em modo não interativo. Os testes já usam respostas gravadas.

## Decisão
- `llm.provider` no `sentinela.yaml` escolhe o provider. O padrão agora é `claude_code`. Os três
  implementam `llm.Caller` (`Call`, `Batch`, `Budget`, `Provider`), e agentes e orquestrador só
  conhecem a interface.
- `anthropic_api`: o cliente das ADRs 0002 a 0004 (SDK, Batch API, prompt caching, custo por
  token, orçamento em dólar).
- `claude_code`: uma chamada `claude -p` por item, com flags conferidas em `claude --help`
  (2.1.296) e numa chamada real:
  `--output-format json --model <id> --tools "" --strict-mcp-config --no-session-persistence
  --system-prompt <prompt do agente> --effort <nível> --json-schema <schema de saída>`.
  - A entrada do agente vai por stdin, porque o Linux limita cada argumento a 128 KB.
  - A resposta sai de `structured_output` (ou de `result`). Recusa é `stop_reason=refusal` ou
    `safety_stops` não vazio; `is_error` vira erro.
  - O provider valida a saída contra o mesmo JSON Schema do agente. Se falhar, repete uma vez com o
    erro de validação no prompt; se falhar de novo, devolve `ErrInvalidOutput` e o item é pulado com
    motivo.
  - Sem Batch: `Batch` roda os itens com concorrência `llm.claude_code.concurrency` (padrão 2).
  - Toda variável `ANTHROPIC_*` sai do ambiente do subprocesso. Com `ANTHROPIC_API_KEY` presente
    (o `.env` tem) o CLI cobraria a API em vez de usar a assinatura. `--bare` não serve, porque só
    autentica por chave de API.
  - O CLI roda num diretório vazio (`.sentinela/claude-cwd`), para não carregar o `CLAUDE.md`
    de nenhum projeto.
  - Em `llm_calls`: `provider = claude_code`, `mode = cli`, tokens do `usage` do CLI e custo 0.
    O `total_cost_usd` que o CLI informa é nocional (assinatura) e não é gravado.
- `replay`: lê `llm.replay.dir/<agente>/<chave>.json` (chave = hash da versão do prompt, do
  sistema e da entrada) ou, se não houver, `<agente>/default.json`. Com
  `llm.claude_code.record_dir`, o provider `claude_code` grava as respostas válidas nesse formato.
- Limites: o orçamento em dólar só vale para `anthropic_api`. Para `claude_code` e `replay`, o
  limite é `llm.claude_code.max_calls_per_run` (padrão 200), contando cada tentativa.
  `ErrCallLimitExceeded` embrulha `ErrBudgetExceeded`, então a rodada aborta como `aborted_budget`.
  A estimativa prévia passa a contar chamadas (esperadas e pior caso) e barra a rodada se as
  esperadas passarem do limite.
- Diagnóstico: com `SENTINELA_CLAUDE_DEBUG_DIR`, a saída bruta de cada chamada é salva em disco.
  Foi assim que se achou o primeiro bug real: `safety_stops` é uma contagem (`0`), não uma lista, e
  toda chamada estava sendo tratada como recusa. `TestRealClaudeCodeCall` (com
  `SENTINELA_REAL_CLAUDE=1`) faz uma chamada real ao CLI para conferir o contrato.
- Migration 00006: coluna `provider` e modo `cli` em `llm_calls`.

## Consequências
- O provider `claude_code` não tem Batch API nem o desconto de 50%, e o prompt caching fica a
  cargo do CLI. É mais lento (uma chamada por item, poucas em paralelo) e está sujeito aos limites
  de uso da assinatura.
- O `CLAUDE.md` do usuário (`~/.claude`) ainda pode ser carregado pelo CLI.
- O prompt de sistema do agente substitui o padrão do Claude Code.
