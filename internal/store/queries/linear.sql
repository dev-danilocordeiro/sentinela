-- name: PendingFindings :many
-- Achados aprovados pelo Judge que ainda não têm issue principal no Linear, na ordem de publicação.
SELECT f.id, f.run_id, f.fingerprint, f.repo, f.module, f.owners, f.routing, f.category, f.severity,
       f.confidence, f.title, f.context, f.evidence, f.signals, f.proposed_fix, f.acceptance_criteria,
       f.scope_paths, f.related_modules, f.judge_confidence, f.judge_reason,
       COALESCE(f.premise, '')::text AS premise, COALESCE(f.premise_answer, '')::text AS premise_answer
FROM findings f
WHERE f.repo = $1 AND f.state = 'open'
  AND NOT EXISTS (SELECT 1 FROM linear_issues li WHERE li.finding_id = f.id AND li.role = 'primary')
ORDER BY CASE f.severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 ELSE 3 END,
         f.judge_confidence DESC NULLS LAST, f.id;

-- name: InsertLinearIssue :one
INSERT INTO linear_issues (finding_id, run_id, role, team, module, linear_team, linear_id, identifier, url, state_name, state_type, synced_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, now())
ON CONFLICT (linear_id) DO UPDATE SET synced_at = now()
RETURNING *;

-- name: SetFindingLinearIssue :exec
UPDATE findings SET linear_issue_id = $2, updated_at = now() WHERE id = $1;

-- name: CountTeamIssues :one
SELECT count(*) FROM linear_issues WHERE team = $1;

-- name: ListLinearIssuesByRun :many
SELECT li.*, f.title, f.category, f.severity, f.fingerprint
FROM linear_issues li JOIN findings f ON f.id = li.finding_id
WHERE li.run_id = $1
ORDER BY li.id;

-- name: ListLinearIssuesByFindings :many
SELECT * FROM linear_issues WHERE finding_id = ANY(sqlc.arg(finding_ids)::bigint[]) ORDER BY id;

-- name: TrackedIssues :many
-- Issues principais que o polling sincroniza: as de achado que ainda espera decisão humana e as
-- que ainda não chegaram a um estado final no Linear. A segunda parte cobre o achado resolvido
-- pelo merge do PR (fixed) cuja issue só depois vai para Done: sem ela, o estado da issue no
-- banco ficava parado (visto na EX-12).
SELECT li.linear_id, li.identifier, li.state_type, li.finding_id, f.state AS finding_state
FROM linear_issues li JOIN findings f ON f.id = li.finding_id
WHERE li.role = 'primary' AND f.merged_into IS NULL
  AND (f.state IN ('open', 'executing') OR li.state_type NOT IN ('completed', 'canceled', 'duplicate'))
ORDER BY li.id;

-- name: LinearIssueByLinearID :one
SELECT li.*, f.state AS finding_state, f.repo, f.module AS finding_module, f.category, f.fingerprint,
       COALESCE(f.premise, '')::text AS premise, COALESCE(f.premise_answer, '')::text AS premise_answer
FROM linear_issues li JOIN findings f ON f.id = li.finding_id
WHERE li.linear_id = $1;

-- name: UpdateLinearIssueState :exec
UPDATE linear_issues SET state_name = $2, state_type = $3, synced_at = now() WHERE linear_id = $1;

-- name: ResolveFinding :one
-- Muda o estado de um achado ainda pendente; devolve nada se outro processo já resolveu.
UPDATE findings SET state = sqlc.arg(state), resolution_reason = sqlc.arg(reason), updated_at = now()
WHERE id = sqlc.arg(id) AND state IN ('open', 'executing')
RETURNING id;

-- name: InsertOutcome :exec
INSERT INTO outcomes (finding_id, kind, reason, actor) VALUES ($1, $2, $3, $4);

-- name: InsertSuppression :one
INSERT INTO suppressions (repo, module, category, fingerprint, pattern, reason, finding_id, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: SuppressionsByFinding :many
SELECT * FROM suppressions WHERE finding_id = $1 ORDER BY id;

-- name: InsertWebhookEvent :one
-- Dedup por (provider, event_id): devolve nada se o evento já foi recebido.
INSERT INTO webhook_events (provider, event_id, event_type, payload)
VALUES ($1, $2, $3, $4)
ON CONFLICT (provider, event_id) DO NOTHING
RETURNING id;

-- name: MarkWebhookEventProcessed :exec
UPDATE webhook_events SET processed_at = now() WHERE id = $1;

-- name: MarkFindingDuplicate :one
-- Issue marcada como Duplicate no Linear: o achado passa a acompanhar o original (merged_into)
-- e assume o estado dele; sem original conhecido, fica stale. Só vale para achado ainda pendente.
UPDATE findings d
SET merged_into = sqlc.narg(original_id),
    state = COALESCE((SELECT o.state FROM findings o WHERE o.id = sqlc.narg(original_id)), 'stale'),
    resolution_reason = sqlc.arg(reason), updated_at = now()
WHERE d.id = sqlc.arg(id) AND d.state IN ('open', 'executing') AND d.merged_into IS NULL
RETURNING d.id, d.state;

-- name: PropagateMergedState :exec
-- Os achados mesclados num original (Duplicate humano) acompanham o estado dele.
UPDATE findings d SET state = o.state, updated_at = now()
FROM findings o
WHERE o.id = $1 AND d.merged_into = o.id AND d.state <> o.state;

-- name: InsertHumanOutcome :exec
INSERT INTO outcomes (finding_id, kind, reason, actor, decided_by, executed_by) VALUES ($1, $2, $3, $4, 'human', 'human');

-- name: SetPremiseAnswer :one
-- Resposta humana à pergunta de um achado com premissa de produto (label aplicada no Linear).
UPDATE findings SET premise_answer = sqlc.arg(answer), updated_at = now()
WHERE id = sqlc.arg(id) AND premise IS NOT NULL AND premise_answer IS NULL
RETURNING id;

-- name: WebhookEventProcessed :one
-- O evento já foi aplicado? Um evento gravado sem processed_at (o Linear falhou no meio) volta a
-- ser aplicado no poll ou na reentrega seguinte.
SELECT EXISTS (SELECT 1 FROM webhook_events WHERE provider = $1 AND event_id = $2 AND processed_at IS NOT NULL)::bool;

-- name: MarkEventProcessed :exec
-- Grava o evento como aplicado, na transação da decisão (ou logo depois, quando não há decisão).
-- Idempotente: um evento já processado mantém a data da primeira vez.
INSERT INTO webhook_events (provider, event_id, event_type, payload, processed_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (provider, event_id) DO UPDATE SET processed_at = COALESCE(webhook_events.processed_at, now());

-- name: HasHumanAccepted :one
-- O achado já tem um accepted humano (resposta "não é intencional" ou aceite)? Concluir a issue
-- depois não grava um segundo (revisão 2026-10-11, M1).
SELECT EXISTS (SELECT 1 FROM outcomes WHERE finding_id = $1 AND kind = 'accepted' AND decided_by = 'human')::bool;

-- name: Installation :one
-- Id desta instalação, que vai no marcador das issues (migration 00018).
SELECT id, legacy_markers_until FROM installation;

-- name: LinearIssuesOfFinding :many
SELECT * FROM linear_issues WHERE finding_id = $1 ORDER BY id;

-- name: DeleteLinearIssue :exec
DELETE FROM linear_issues WHERE id = $1;

-- name: InsertFindingUnlink :exec
INSERT INTO finding_unlinks (finding_id, linear_id, identifier, url, role, reason, actor)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: ClearFindingLinearIssue :exec
UPDATE findings SET linear_issue_id = NULL, updated_at = now() WHERE id = $1;

-- name: InstallationHome :one
-- Diretório registrado da instalação dona deste banco (vazio: nenhum ainda).
SELECT COALESCE(home_dir, '')::text FROM installation;

-- name: ClaimInstallationHome :execrows
-- Registra o diretório, só se nenhum foi registrado antes.
UPDATE installation SET home_dir = sqlc.arg(home_dir) WHERE home_dir IS NULL;

-- name: HasHistory :one
-- O banco já tem histórico de uma instalação (rodadas, execuções ou issues)?
SELECT EXISTS (SELECT 1 FROM analysis_runs) OR EXISTS (SELECT 1 FROM executions) OR EXISTS (SELECT 1 FROM linear_issues);
