# ADR 0043: Módulo com override para arquivos transversais

- Status: aceita
- Data: 2026-10-11

## Contexto

Entre os módulos da config que casam com um caminho, vence o glob mais longo. Para arquivos
transversais, que um time cuida em qualquer lugar do repo (Dockerfile, CI, infra), isso é
frágil: `**/Dockerfile` (13 caracteres) perde para `modules/cobranca/**` (19). O cenário acme
dependia de `modules/*/Dockerfile` ser 1 caractere mais longo que os módulos de pagamentos; um
módulo de nome mais longo quebraria a regra sem ninguém perceber.

## Decisão

- **`override: true` num módulo** (`repos[].modules[]` ou `modules` de um arquivo de time): o
  módulo vence os módulos comuns que também casam, qualquer que seja o tamanho do glob. Entre
  overrides, vale o mais longo; no empate, o primeiro declarado.
- **A regra atual não muda:** sem `override`, continua valendo o glob mais longo. Nenhuma config
  existente muda de comportamento.
- **O cenário acme** passa a usar `**/Dockerfile` com `override: true` no arquivo da plataforma.
  O teste do exemplo acrescenta um módulo de nome longo
  (`modules/conciliacao-bancaria-internacional/**`) e confere que o Dockerfile dele continua indo
  para a plataforma, e que o resto do módulo fica com pagamentos.
- **O `owners explain`** diz quando o módulo venceu por override.

## Consequências

- **Recomendação** (no guia): use override para Dockerfile, CI, infra e outros arquivos
  transversais, em vez de depender do tamanho do glob.
- **Override não muda o CODEOWNERS:** ele vale só entre os módulos da config (passo 1). No
  CODEOWNERS, a última regra que casa continua vencendo.
