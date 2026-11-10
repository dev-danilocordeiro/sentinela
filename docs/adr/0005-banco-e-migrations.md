# ADR 0005: Banco, migrations e testes de integração

- Status: aceita
- Data: 2026-10-09

## Decisão
- Imagem `pgvector/pgvector:pg16`. `docker compose up` sobe só o Postgres; o app fica no profile
  `app` (`docker compose --profile app up --build`), que hoje roda `migrate up`.
- Migrations goose em SQL, embutidas no binário (`embed`), uma por grupo de tabelas, todas com `Down`.
  `sentinela migrate up | down [--all] | status`.
- Tabelas: `analysis_runs`, `findings` (`embedding vector(1024)` com índice HNSW de cosseno, `state`
  com os seis estados, `resolution_reason`, `routing` JSONB), `suppressions`, `executions` (índice
  parcial para a fila e no máximo uma execução ativa por achado), `webhook_events` (único por
  `provider, event_id`), `outcomes`, `llm_calls`, `prompt_versions`.
- sqlc (pgx/v5) gera `internal/store/db` a partir das migrations e de `internal/store/queries`.
  O código gerado é versionado; regenere com `sqlc generate`.
- Teste de integração do store roda só com `SENTINELA_TEST_DATABASE_URL` (banco descartável);
  sem ela, é pulado, para que `go test ./...` passe sem Docker. Testcontainers ficou de fora para
  não puxar dependências pesadas na Fase 1; reavaliar na Fase 4 (fila com SKIP LOCKED).

## Consequências
Depois da Fase 2, mudanças no esquema exigem perguntar antes (regra do CLAUDE.md).
