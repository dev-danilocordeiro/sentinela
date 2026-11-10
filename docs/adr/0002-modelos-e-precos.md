# ADR 0002: IDs de modelo e tabela de preços configurável

- Status: aceita
- Data: 2026-10-09

## Contexto
Conferido na documentação da Anthropic em 2026-10-09
(platform.claude.com/docs/en/about-claude/models/overview e /pricing):

| Papel | Modelo | Entrada $/MTok | Saída $/MTok | Leitura de cache $/MTok |
|---|---|---|---|---|
| Triagem, curadoria | `claude-haiku-5-5` | 0.10 (0.50 acima de 100K) | 0.50 (2.50 acima de 100K) | 0.01 |
| Análise, juiz, executor | `claude-sonnet-5-5` | 2.00 | 10.00 | 0.20 |
| Review do gate, escalonamento | `claude-opus-5-5` | 4.00 | 20.00 | 0.20 |

Escrita de cache: 1,25x (TTL de 5 min) e 2x (TTL de 1 h) o preço de entrada. Batch: 50%.

## Decisão
- Os IDs já estavam corretos no `sentinela.yaml`. Os preços ficam em `pricing:` no YAML, não no código,
  para atualizar sem recompilar. `config validate` falha se um modelo configurado não tiver preço.
- Faixas (`tiers`) valem para o prompt inteiro (entrada + escrita + leitura de cache), o que
  cobre a mudança de faixa do Haiku 5.5 acima de 100K tokens.
- A leitura de cache acima de 100K no Haiku (0,05) segue a regra de 0,1x da entrada. A documentação não
  traz esse número explícito; confira antes da Fase 2.
- Dinheiro é `int64` em nanodólares (`llm.NanoUSD`) e `NUMERIC(18, 9)` no banco, sem acumular erro de float.

## Consequências
Quando a Anthropic mudar preços, basta editar o YAML. Custo de chamadas antigas não é recalculado:
o valor gravado em `llm_calls` é o da tabela vigente na hora da chamada.
