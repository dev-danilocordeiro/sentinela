# ADR 0001: Layout do repo e stack da Fase 1

- Status: aceita
- Data: 2026-10-09

## Contexto
A especificação fixa Go, cobra, Postgres 16 + pgvector, goose, pgx + sqlc e a estrutura de diretórios.
O Go instalado é 1.27 e o golangci-lint é v2.

## Decisão
- Módulo `github.com/dev-danilocordeiro/sentinela`, binário único em `cmd/sentinela`.
- Os subcomandos ficam em `internal/cli`, que só faz a fiação; a lógica fica nos pacotes de domínio.
- O resolvedor de ownership e o parser de CODEOWNERS ficam em `internal/repo`, conforme a estrutura
  (CODEOWNERS é responsabilidade de `repo`); `internal/config` só carrega e valida o YAML.
- Na Fase 1 existem só `migrate`, `config` e `owners`. `scan`, `serve`, `worker` e `report` entram
  nas fases que os implementam, para não haver comando que não faz nada.
- Versões fixadas: anthropic-sdk-go v1.80.0, cobra v1.10.2, goose v3.28.0, pgx v5.11.0,
  doublestar v4.10.2, yaml.v3, pgvector-go v0.4.1.
- `sentinela.yaml` fica na raiz e serve como config padrão e fixture dos testes;
  `sentinela-setup/CODEOWNERS` é lido direto pelos testes, sem cópia.

## Consequências
Os testes quebram se alguém editar o `sentinela.yaml` de forma incompatível com os casos de borda.
Isso é intencional: a config real é o contrato que o resolvedor precisa respeitar.
