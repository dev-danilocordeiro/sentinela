-- name: OutcomesSince :many
-- Decisões humanas desde um instante, com o achado, para o Feedback Curator. Vale quem decidiu
-- (decided_by): decisão humana executada por agente conta; decided_by = 'agent' fica fora.
SELECT o.id, o.kind, COALESCE(o.reason, '')::text AS reason, COALESCE(o.actor, '')::text AS actor, o.occurred_at,
       f.id AS finding_id, f.fingerprint, f.module, f.category, f.severity, f.title, f.confidence,
       f.judge_confidence, f.evidence
FROM outcomes o JOIN findings f ON f.id = o.finding_id
WHERE f.repo = $1 AND o.occurred_at > $2 AND o.decided_by = 'human' AND o.kind <> 'duplicate_of'
ORDER BY o.occurred_at;

-- name: ReposWithOutcomesSince :many
SELECT DISTINCT f.repo FROM outcomes o JOIN findings f ON f.id = o.finding_id
WHERE o.occurred_at > $1 AND o.decided_by = 'human' AND o.kind <> 'duplicate_of'
ORDER BY f.repo;

-- name: ActiveExamples :many
-- Confirmados por humano primeiro.
SELECT * FROM feedback_examples WHERE repo = $1 AND active
ORDER BY (confirmed_at IS NOT NULL) DESC, created_at DESC, id LIMIT $2;

-- name: DeactivateExamples :exec
-- A curadoria substitui os exemplos ativos, menos os que um humano confirmou.
UPDATE feedback_examples SET active = false WHERE repo = $1 AND active AND confirmed_at IS NULL;

-- name: InsertExample :exec
INSERT INTO feedback_examples (repo, module, category, finding_id, outcome, title, lesson)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: UpsertThreshold :exec
INSERT INTO thresholds (repo, module, category, min_confidence, reason)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (repo, module, category) DO UPDATE
SET min_confidence = EXCLUDED.min_confidence, reason = EXCLUDED.reason, updated_at = now();

-- name: ThresholdsByRepo :many
SELECT * FROM thresholds WHERE repo = $1 ORDER BY module, category;

-- name: LastCuratorCall :one
SELECT COALESCE(max(created_at), 'epoch'::timestamptz)::timestamptz AS at FROM llm_calls WHERE agent = 'curator';

-- name: AgentDecisions :many
-- Desfechos decididos por agente: não deveriam existir (princípio 6). O config validate avisa.
SELECT o.id, o.kind, COALESCE(o.reason, '')::text AS reason, o.occurred_at, f.repo, f.title
FROM outcomes o JOIN findings f ON f.id = o.finding_id
WHERE o.decided_by = 'agent'
ORDER BY o.id;
