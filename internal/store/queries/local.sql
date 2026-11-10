-- Tracker local (ADR 0038): achados no relatório em reports/ e decisões pela CLI.

-- name: LocalFindings :many
-- Achados do relatório (sem os mesclados), com a última execução e se uma pessoa já aceitou.
SELECT sqlc.embed(f),
       COALESCE(le.id, 0)::bigint AS exec_id, COALESCE(le.status, '')::text AS exec_status,
       COALESCE(le.decision, '')::text AS exec_decision, COALESCE(le.pr_url, '')::text AS pr_url,
       COALESCE(le.pr_state, '')::text AS pr_state,
       EXISTS (SELECT 1 FROM outcomes o WHERE o.finding_id = f.id AND o.kind = 'accepted' AND o.decided_by = 'human')::bool AS accepted
FROM findings f
LEFT JOIN LATERAL (SELECT * FROM executions e WHERE e.finding_id = f.id ORDER BY e.id DESC LIMIT 1) le ON true
WHERE f.merged_into IS NULL AND (sqlc.arg(repo)::text = '' OR f.repo = sqlc.arg(repo)::text)
ORDER BY f.repo, CASE f.state WHEN 'open' THEN 0 WHEN 'executing' THEN 1 ELSE 2 END, f.id;

-- name: InsertDecisionOutcome :exec
-- Decisão humana registrada pela CLI. executed_by = agent quando um agente executou a pedido
-- explícito de quem decidiu (princípio 6), com a nota.
INSERT INTO outcomes (finding_id, kind, reason, actor, decided_by, executed_by, note)
VALUES (sqlc.arg(finding_id), sqlc.arg(kind), sqlc.arg(reason), sqlc.arg(actor), 'human', sqlc.arg(executed_by), sqlc.arg(note));

-- name: FindingOutcomes :many
SELECT * FROM outcomes WHERE finding_id = $1 ORDER BY id;

-- name: FindingExecutions :many
SELECT * FROM executions WHERE finding_id = $1 ORDER BY id;

-- name: LastExecutionForFinding :one
SELECT * FROM executions WHERE finding_id = $1 ORDER BY id DESC LIMIT 1;

-- name: LockFinding :one
-- Trava o achado até o fim da transação: a conferência "já aceito" e o insert do accepted não se
-- intercalam entre dois processos (M9 da revisão de 2026-10-11).
SELECT state FROM findings WHERE id = $1 FOR UPDATE;
