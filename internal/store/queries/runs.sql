-- name: CreateRun :one
INSERT INTO analysis_runs (repo, base_sha, head_sha, budget_usd, mode)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetRun :one
SELECT * FROM analysis_runs WHERE id = $1;

-- name: LastCompletedRun :one
-- Última rodada que terminou a análise do repo; base do diff incremental.
SELECT * FROM analysis_runs
WHERE repo = $1 AND status IN ('succeeded', 'no_changes') AND head_sha IS NOT NULL
  AND mode IN ('full', 'incremental') -- notas e rejulgamento não analisam o repo: não servem de base
ORDER BY id DESC
LIMIT 1;

-- name: AddRunCost :one
UPDATE analysis_runs SET cost_usd = cost_usd + sqlc.arg(delta)::numeric
WHERE id = sqlc.arg(id)
RETURNING cost_usd;

-- name: FinishRun :exec
UPDATE analysis_runs SET status = $2, error = $3, stats = $4, finished_at = now()
WHERE id = $1;

-- name: InsertRunItem :exec
INSERT INTO run_items (run_id, agent, item_key, status, model, output, reason)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (run_id, agent, item_key) DO UPDATE
SET status = EXCLUDED.status, model = EXCLUDED.model, output = EXCLUDED.output, reason = EXCLUDED.reason;

-- name: ListRunItems :many
SELECT * FROM run_items WHERE run_id = $1 ORDER BY agent, item_key;

-- name: CostByAgent :many
SELECT agent,
       count(*)::bigint                               AS calls,
       COALESCE(sum(input_tokens), 0)::bigint          AS input_tokens,
       COALESCE(sum(output_tokens), 0)::bigint         AS output_tokens,
       COALESCE(sum(cache_read_tokens), 0)::bigint     AS cache_read_tokens,
       COALESCE(sum(cache_write_5m_tokens + cache_write_1h_tokens), 0)::bigint AS cache_write_tokens,
       COALESCE(sum(cost_usd), 0)::numeric             AS cost_usd,
       string_agg(DISTINCT model, ', ')::text          AS models,
       string_agg(DISTINCT provider, ', ')::text       AS providers,
       COALESCE(sum(latency_ms), 0)::bigint            AS latency_ms
FROM llm_calls
WHERE run_id = $1
GROUP BY agent
ORDER BY agent;

-- name: PriorJudgeRejections :many
-- Decisões anteriores do Judge que não viraram achado (descartados e mesclados) no repo, para
-- que a próxima rodada não reapresente o mesmo problema com outras palavras.
SELECT ri.run_id, ri.item_key, ri.output
FROM run_items ri JOIN analysis_runs ar ON ar.id = ri.run_id
WHERE ar.repo = $1 AND ri.agent = 'judge' AND ri.status = 'ok'
  AND ri.output->>'decision' IN ('discard', 'merge')
ORDER BY ri.id DESC
LIMIT $2;

-- name: JudgeRelatedTo :one
-- Fingerprints de achados abertos que o Judge marcou como sobrepostos a este (o escopo do novo
-- foi reduzido ao que eles não cobrem); o publisher linka as issues como relacionadas.
SELECT COALESCE(output->'related_to', '[]'::jsonb)::jsonb AS related_to
FROM run_items
WHERE run_id = $1 AND agent = 'judge' AND item_key = $2 AND status = 'ok';

-- name: LastRunPerRepo :many
-- sentinela status: a rodada mais recente de cada repo.
SELECT DISTINCT ON (repo) id, repo, status, started_at, finished_at, COALESCE(error, '')::text AS error
FROM analysis_runs ORDER BY repo, id DESC;

-- name: ExecutionStatusCounts :many
SELECT status, count(*)::int AS n FROM executions GROUP BY status ORDER BY status;

-- name: OpenFindingsWithoutIssue :one
-- Achados abertos sem issue: pendentes pelo limite por time ou gravados em modo sombra.
SELECT count(*)::int FROM findings f
WHERE f.state = 'open' AND f.merged_into IS NULL
  AND NOT EXISTS (SELECT 1 FROM linear_issues li WHERE li.finding_id = f.id);

-- name: ReposWithCompletedRun :many
-- scan --all: repos que já têm base para o diff incremental (mesma regra de LastCompletedRun).
SELECT DISTINCT repo FROM analysis_runs
WHERE status IN ('succeeded', 'no_changes') AND head_sha IS NOT NULL AND mode IN ('full', 'incremental');

-- name: MCPCallStats :one
-- sentinela status no modo MCP (ADR 0039): chamadas `claude -p` aos MCPs nos últimos 7 dias. Um
-- ciclo de leitura é uma listagem (custom_id poll); as leituras incluem as de issues fora da
-- listagem e as de motivo de recusa.
SELECT count(*)::int AS total,
       count(*) FILTER (WHERE custom_id = 'poll')::int AS cycles,
       count(*) FILTER (WHERE custom_id IN ('poll', 'get', 'closure', 'duplicate'))::int AS reads,
       count(*) FILTER (WHERE status <> 'succeeded')::int AS failed,
       COALESCE(extract(epoch FROM now() - min(created_at)), 0)::float8 AS span_seconds,
       COALESCE(avg(latency_ms), 0)::float8 AS avg_latency_ms
FROM llm_calls
WHERE agent IN ('linear_mcp', 'slack_mcp') AND created_at > now() - interval '7 days';

-- name: CostByRepo30d :many
-- sentinela status: custo da descoberta por repo nos últimos 30 dias (dólar na API; na assinatura,
-- o equivalente que o Claude Code reporta, quando reporta).
SELECT r.repo, COALESCE(sum(c.cost_usd), 0)::numeric AS cost_usd, count(*)::int AS calls
FROM llm_calls c JOIN analysis_runs r ON r.id = c.run_id
WHERE c.created_at > now() - interval '30 days'
GROUP BY r.repo ORDER BY 2 DESC, 1;

-- name: CostByTeam30d :many
-- sentinela status: custo das correções por time dono (roteamento do achado) nos últimos 30 dias.
SELECT COALESCE(f.routing->>'team', '')::text AS team, COALESCE(sum(c.cost_usd), 0)::numeric AS cost_usd,
       count(DISTINCT e.id)::int AS jobs
FROM llm_calls c
JOIN executions e ON c.custom_id = 'execution:' || e.id::text
JOIN findings f ON f.id = e.finding_id
WHERE c.created_at > now() - interval '30 days'
GROUP BY 1 ORDER BY 2 DESC, 1;

-- name: SetRunPublishPause :exec
-- Publicação parada no limite de uso da assinatura (ADR 0055): grava em stats.issues.paused_until
-- quando a cota volta. Usado pelo scan --all, que publica depois de fechar as rodadas.
UPDATE analysis_runs
SET stats = jsonb_set(stats, '{issues}', COALESCE(stats->'issues', '{}'::jsonb) || jsonb_build_object('paused_until', sqlc.arg(until)::timestamptz))
WHERE id = sqlc.arg(id);

-- name: PausedPublications :many
-- Repos cuja última rodada de análise parou a publicação no limite de uso, com o horário do reset.
SELECT repo, id, paused_until::timestamptz AS paused_until FROM (
    SELECT DISTINCT ON (repo) repo, id, (stats->'issues'->>'paused_until') AS paused_until
    FROM analysis_runs
    WHERE mode IN ('full', 'incremental') AND status IN ('succeeded', 'no_changes') AND head_sha IS NOT NULL
    ORDER BY repo, id DESC
) last
WHERE paused_until IS NOT NULL
ORDER BY repo;
