-- name: EnqueueExecution :one
-- Enfileira uma execução. O índice parcial executions_one_active_idx garante no máximo uma
-- ativa (queued ou running) por achado; se já houver, não devolve nada.
INSERT INTO executions (finding_id, kind, repo, issue_identifier, linear_issue_id, rework_note, previous_id, branch, pr_number, pr_url, pr_state)
VALUES (sqlc.arg(finding_id), sqlc.arg(kind), sqlc.arg(repo), sqlc.arg(issue_identifier), sqlc.arg(linear_issue_id),
        sqlc.narg(rework_note), sqlc.narg(previous_id), sqlc.narg(branch), sqlc.narg(pr_number), sqlc.narg(pr_url), sqlc.narg(pr_state))
ON CONFLICT (finding_id) WHERE status IN ('queued', 'running') DO NOTHING
RETURNING *;

-- name: HasExecution :one
-- O gatilho (label sentinela-fix) só enfileira se o achado nunca teve execução não cancelada;
-- uma nova tentativa é decisão humana (label reaplicada, job retry ou job rework; ADR 0029).
SELECT EXISTS (SELECT 1 FROM executions WHERE finding_id = $1 AND status <> 'canceled')::bool;

-- name: LockQueue :exec
-- Serializa o planejamento da fila entre workers (goroutines e processos) até o fim da transação.
SELECT pg_advisory_xact_lock(hashtext('sentinela.executions.queue'));

-- name: QueueSnapshot :many
-- O que a fila consciente de conflitos precisa (ADR 0031): jobs na fila ou rodando e os que ainda
-- seguram arquivos por um PR aberto, com os caminhos do achado (escopo e evidência).
SELECT e.id, e.finding_id, e.repo, e.status, e.issue_identifier, e.pr_number, e.pr_state,
       (e.run_after <= now())::bool AS ready, e.wait_reason, f.scope_paths, f.evidence,
       EXISTS (SELECT 1 FROM executions o WHERE o.finding_id = e.finding_id AND o.pr_state = 'open')::bool AS finding_has_open_pr
FROM executions e JOIN findings f ON f.id = e.finding_id
WHERE e.status IN ('queued', 'running') OR e.pr_state = 'open'
ORDER BY e.run_after, e.id;

-- name: ClaimExecutionByID :one
-- Pega um job escolhido pelo planejamento, se ele ainda estiver na fila.
UPDATE executions SET status = 'running', attempt = attempt + 1, locked_at = now(), started_at = now(),
    wait_reason = NULL, updated_at = now()
WHERE id = (SELECT id FROM executions WHERE executions.id = sqlc.arg(id) AND status = 'queued' FOR UPDATE SKIP LOCKED)
RETURNING *;

-- name: SetWaitReason :exec
UPDATE executions SET wait_reason = sqlc.narg(wait_reason), updated_at = now()
WHERE id = sqlc.arg(id) AND status = 'queued' AND wait_reason IS DISTINCT FROM sqlc.narg(wait_reason);

-- name: SetExecutionWorkspace :exec
UPDATE executions SET worktree = $2, job_dir = $3, branch = $4, updated_at = now() WHERE id = $1;

-- name: SetExecutionSession :exec
UPDATE executions SET session_id = $2, updated_at = now() WHERE id = $1;

-- name: SetExecutionSessionLog :exec
-- Arquivos JSONL da sessão do Claude Code (principal e subagentes), para retomar e auditar.
UPDATE executions SET session_log = sqlc.arg(session_log), updated_at = now() WHERE id = sqlc.arg(id);

-- name: SetExecutionResumeNote :exec
UPDATE executions SET resume_note = sqlc.arg(resume_note), updated_at = now() WHERE id = sqlc.arg(id);

-- name: FinishExecution :exec
UPDATE executions
SET status = sqlc.arg(status), decision = sqlc.narg(decision), decision_reason = sqlc.narg(decision_reason),
    rounds = sqlc.arg(rounds), error = sqlc.narg(error), cost_usd = sqlc.arg(cost_usd), num_turns = sqlc.arg(num_turns),
    artifacts = sqlc.arg(artifacts), head_sha = COALESCE(sqlc.narg(head_sha), head_sha), model = sqlc.narg(model),
    locked_at = NULL, finished_at = now(), updated_at = now()
WHERE id = sqlc.arg(id);

-- name: SetExecutionPR :exec
UPDATE executions SET pr_number = $2, pr_url = $3, pr_state = 'open', head_sha = $4, updated_at = now() WHERE id = $1;

-- name: SetExecutionGate :exec
UPDATE executions SET gate = $2, gate_reasons = $3, updated_at = now() WHERE id = $1;

-- name: GetExecution :one
SELECT * FROM executions WHERE id = $1;

-- name: ListExecutions :many
SELECT * FROM executions ORDER BY id DESC LIMIT $1;

-- name: InsertReview :exec
INSERT INTO reviews (execution_id, round, lens, verdict, findings) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (execution_id, round, lens) DO UPDATE SET verdict = EXCLUDED.verdict, findings = EXCLUDED.findings;

-- name: ReviewsByExecution :many
SELECT * FROM reviews WHERE execution_id = $1 ORDER BY round, lens;

-- name: ApproveExecution :one
-- sentinela job approve: só um job em ESCALATE, uma vez.
UPDATE executions SET approved_by = sqlc.arg(approved_by), approved_at = now(), updated_at = now()
WHERE id = sqlc.arg(id) AND decision = 'escalate' AND approved_at IS NULL
RETURNING *;

-- name: OpenPRExecutions :many
-- Execuções com PR aberto: o shepherd e o acompanhamento de desfecho olham estas. A mais
-- recente de cada branch (um rework reaproveita o PR da anterior).
SELECT DISTINCT ON (repo, branch) * FROM executions
WHERE pr_state = 'open' AND status = 'succeeded'
ORDER BY repo, branch, id DESC;

-- name: SetPRState :execrows
-- Só sai de open: é a trava contra dois acompanhamentos (serve e worker) gravando o mesmo
-- desfecho. O segundo espera a transação do primeiro e não muda nenhuma linha.
UPDATE executions SET pr_state = $2, updated_at = now() WHERE repo = $1 AND pr_number = $3 AND pr_state = 'open';

-- name: RecordShepherdCycle :one
UPDATE executions
SET shepherd_cycles = shepherd_cycles + 1, shepherd_handled = shepherd_handled || sqlc.arg(event)::jsonb,
    head_sha = COALESCE(sqlc.narg(head_sha), head_sha), cost_usd = cost_usd + sqlc.arg(cost_usd), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING shepherd_cycles;

-- name: MarkShepherdHandled :exec
UPDATE executions SET shepherd_handled = shepherd_handled || sqlc.arg(event)::jsonb, updated_at = now() WHERE id = sqlc.arg(id);

-- name: LensMetrics :many
-- Por lente: reviews, bloqueios (CHANGES), jobs bloqueados e bloqueios confirmados pelo humano
-- (o humano também pediu mudança no PR, ou fechou sem merge).
SELECT r.lens,
       count(*)::int AS reviews,
       count(*) FILTER (WHERE r.verdict = 'CHANGES')::int AS blocks,
       count(DISTINCT r.execution_id) FILTER (WHERE r.verdict = 'CHANGES')::int AS jobs_blocked,
       count(DISTINCT r.execution_id) FILTER (
           WHERE r.verdict = 'CHANGES'
             AND (e.pr_state = 'closed' OR e.shepherd_handled @> '[{"kind": "review"}]'::jsonb)
       )::int AS confirmed
FROM reviews r JOIN executions e ON e.id = r.execution_id
GROUP BY r.lens
ORDER BY r.lens;

-- name: JobMetrics :one
SELECT count(*)::int AS jobs,
       COALESCE(avg(rounds), 0)::float8 AS avg_rounds,
       COALESCE(sum(cost_usd), 0)::float8 AS total_cost,
       COALESCE(avg(cost_usd), 0)::float8 AS avg_cost,
       count(*) FILTER (WHERE decision = 'approve')::int AS approved,
       count(*) FILTER (WHERE decision = 'escalate')::int AS escalated,
       count(*) FILTER (WHERE decision IN ('failed', 'blocked'))::int AS failed
FROM executions WHERE status IN ('succeeded', 'failed');

-- name: ModuleReverts :one
SELECT count(*)::int FROM outcomes o JOIN findings f ON f.id = o.finding_id
WHERE f.repo = $1 AND f.module = $2 AND o.kind = 'reverted';

-- name: GetFinding :one
SELECT * FROM findings WHERE id = $1;

-- name: SetFindingState :exec
UPDATE findings SET state = $2, updated_at = now() WHERE id = $1;

-- name: InsertExecutionOutcome :exec
INSERT INTO outcomes (finding_id, execution_id, kind, reason, actor) VALUES ($1, $2, $3, $4, $5);

-- name: PrimaryIssueForFinding :one
SELECT * FROM linear_issues WHERE finding_id = $1 AND role = 'primary' LIMIT 1;

-- name: TriggerCandidates :many
-- Issues principais com achado aberto e sem execução não cancelada: o worker confere no Linear
-- quais têm a label de gatilho (aplicada por um humano).
SELECT li.linear_id, li.identifier, li.linear_team, li.team, li.url, f.id AS finding_id, f.repo
FROM linear_issues li JOIN findings f ON f.id = li.finding_id
WHERE li.role = 'primary' AND f.state = 'open'
  AND NOT EXISTS (SELECT 1 FROM executions e WHERE e.finding_id = f.id AND e.status <> 'canceled')
ORDER BY li.id;

-- name: RetriggerCandidates :many
-- Issues principais com achado aberto cuja última execução falhou ou foi bloqueada sem PR e sem
-- execução ativa (ADR 0029). Enquanto o pedido não foi consumido, o worker tira a label de
-- gatilho; depois disso, a label de volta é um novo pedido de uma pessoa.
SELECT li.linear_id, li.identifier, li.linear_team, li.team, f.id AS finding_id, f.repo,
       last.id AS previous_id, (last.artifacts ? 'trigger-consumed')::bool AS trigger_consumed
FROM linear_issues li
JOIN findings f ON f.id = li.finding_id
JOIN LATERAL (
    SELECT e.* FROM executions e
    WHERE e.finding_id = f.id AND e.status <> 'canceled'
    ORDER BY e.id DESC LIMIT 1
) last ON true
WHERE li.role = 'primary' AND f.state = 'open'
  AND (last.status = 'failed' OR last.decision = 'blocked')
  AND last.pr_number IS NULL AND last.finished_at IS NOT NULL
ORDER BY li.id;

-- name: MarkTriggerConsumed :exec
-- Registra em artifacts que o worker tirou a label de gatilho depois desta execução (ADR 0029).
UPDATE executions SET artifacts = artifacts || jsonb_build_object('trigger-consumed', sqlc.arg(artifact)::jsonb)
WHERE id = sqlc.arg(id);

-- name: MergedWithoutRevert :many
-- PRs mergeados nos últimos 30 dias sem revert registrado: o acompanhamento procura o revert.
SELECT e.* FROM executions e
WHERE e.pr_state = 'merged' AND e.updated_at > now() - interval '30 days'
  AND NOT EXISTS (SELECT 1 FROM outcomes o WHERE o.execution_id = e.id AND o.kind = 'reverted');

-- name: PreviousExecutionStart :one
-- Quando o job anterior do mesmo achado leu a issue (início, ou criação se não chegou a rodar);
-- os comentários depois disso são instruções novas para o job atual (ADR 0026). Sem job
-- anterior, devolve NULL.
SELECT MAX(COALESCE(started_at, created_at))::timestamptz AS started
FROM executions
WHERE finding_id = $1 AND id < $2;

-- name: LastExecutionAtWorktree :one
-- A execução mais recente que usou este worktree (outra que não a atual): é a dona do que está
-- nele quando um recomeço do zero precisa do caminho (ADR 0034).
SELECT * FROM executions WHERE worktree = sqlc.arg(worktree) AND id <> sqlc.arg(id) ORDER BY id DESC LIMIT 1;

-- name: SetExecutionWorktree :exec
UPDATE executions SET worktree = sqlc.arg(worktree), updated_at = now() WHERE id = sqlc.arg(id);

-- name: WorktreeSweepCandidates :many
-- Limpeza de worktrees (ADR 0037): a execução mais recente de cada caminho ainda não removido,
-- com o fim dela e se o achado tem execução na fila ou rodando (que pode retomar no caminho).
SELECT * FROM (
    SELECT DISTINCT ON (e.worktree) e.id, e.finding_id, e.issue_identifier, e.worktree::text AS worktree,
           e.status, COALESCE(e.pr_state, '')::text AS pr_state,
           COALESCE(e.finished_at, e.updated_at)::timestamptz AS ended_at, e.worktree_removed_at,
           EXISTS (SELECT 1 FROM executions a WHERE a.finding_id = e.finding_id AND a.status IN ('queued', 'running'))::bool AS finding_active
    FROM executions e
    WHERE e.worktree IS NOT NULL AND e.worktree <> ''
    ORDER BY e.worktree, e.id DESC
) latest
WHERE latest.worktree_removed_at IS NULL;

-- name: MarkWorktreeRemoved :exec
-- Marca o caminho como removido em todas as execuções que o usaram; os bytes liberados ficam só
-- na mais recente, para a soma não contar o mesmo diretório duas vezes.
UPDATE executions
SET worktree_removed_at = now(),
    worktree_freed_bytes = CASE WHEN id = sqlc.arg(id) THEN sqlc.arg(freed_bytes)::bigint END
WHERE worktree = sqlc.arg(worktree) AND worktree_removed_at IS NULL;

-- name: WorktreeCleanupTotals :one
-- sentinela status: worktrees removidos e disco liberado, no total e nos últimos 7 dias.
SELECT count(*)::int AS removed,
       COALESCE(sum(worktree_freed_bytes), 0)::bigint AS freed_bytes,
       COALESCE(sum(worktree_freed_bytes) FILTER (WHERE worktree_removed_at > now() - interval '7 days'), 0)::bigint AS freed_week
FROM executions WHERE worktree_freed_bytes IS NOT NULL;

-- name: InsertTriggerRefusal :one
-- Recusa nova de gatilho (ADR 0042); nada se já foi registrada para esta entrada do histórico.
INSERT INTO trigger_refusals (finding_id, linear_issue_id, event_id, actor, allowed)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (event_id) DO NOTHING
RETURNING id;

-- name: CountTriggerRefusals :one
SELECT count(*)::int FROM trigger_refusals;

-- name: ReopenAfterClosedPR :one
-- PR fechado sem merge: o achado volta a open (o time pode pedir a correção de novo) só se ainda
-- estiver em execução, sem recusa nem duplicata humana, sem resposta "é intencional" e com a issue
-- principal ainda aberta. Nos outros casos a decisão humana fica (revisão 2026-10-11, C3).
UPDATE findings f SET state = 'open', updated_at = now()
WHERE f.id = $1 AND f.state = 'executing'
  AND COALESCE(f.premise_answer, '') <> 'intentional'
  AND NOT EXISTS (SELECT 1 FROM outcomes o WHERE o.finding_id = f.id AND o.decided_by = 'human' AND o.kind IN ('rejected', 'duplicate_of'))
  AND NOT EXISTS (SELECT 1 FROM linear_issues li WHERE li.finding_id = f.id AND li.role = 'primary'
                  AND li.state_type IN ('canceled', 'completed', 'duplicate'))
RETURNING f.id;

-- name: TriggerConsumedCounts :one
-- Gatilhos consumidos (ADR 0029): execuções que terminaram sem PR e em que o worker tirou a label
-- de gatilho de uma pessoa (removed) ou registrou que ela já estava fora. Total e últimos 30 dias.
SELECT count(*) FILTER (WHERE artifacts->'trigger-consumed'->>'summary' LIKE '%tirada pelo worker%')::bigint AS removed,
       count(*) FILTER (WHERE artifacts->'trigger-consumed'->>'summary' LIKE '%tirada pelo worker%'
                          AND updated_at > now() - interval '30 days')::bigint AS removed_30d,
       count(*)::bigint AS consumed
FROM executions WHERE artifacts ? 'trigger-consumed';

-- name: RequeueForQuota :execrows
-- Job que parou no limite de uso da assinatura: volta para a fila até o reset, sem failed, com a
-- sessão, o worktree e o diretório do job guardados para continuar (artifacts.quota).
UPDATE executions
SET status = 'queued', run_after = sqlc.arg(run_after), wait_reason = sqlc.arg(wait_reason), locked_at = NULL,
    artifacts = sqlc.arg(artifacts), cost_usd = cost_usd + sqlc.arg(cost_usd), num_turns = num_turns + sqlc.arg(num_turns),
    updated_at = now()
WHERE id = sqlc.arg(id) AND status = 'running';

-- name: QuotaPausedUntil :one
-- Até quando o worker fica sem pegar jobs: o maior run_after dos jobs devolvidos à fila pela cota
-- da assinatura que ainda não venceu. Sobrevive a um restart do worker.
SELECT COALESCE(max(run_after), 'epoch'::timestamptz)::timestamptz AS until
FROM executions
WHERE status = 'queued' AND wait_reason LIKE sqlc.arg(prefix)::text || '%' AND run_after > now();

-- name: JobDirSweepCandidates :many
-- Retenção dos artefatos dos jobs (D-12): execuções terminadas com diretório de job, com o
-- instante do desfecho (o fim do job, ou a última mudança, como o merge ou o fechamento do PR) e
-- se o achado tem execução na fila ou rodando (que pode retomar a sessão pela pasta do job).
SELECT e.id, e.job_dir::text AS job_dir, e.status, COALESCE(e.pr_state, '')::text AS pr_state,
       GREATEST(COALESCE(e.finished_at, e.updated_at), e.updated_at)::timestamptz AS outcome_at,
       EXISTS (SELECT 1 FROM executions a WHERE a.finding_id = e.finding_id AND a.status IN ('queued', 'running'))::bool AS finding_active
FROM executions e
WHERE e.job_dir IS NOT NULL AND e.job_dir <> '' AND e.status IN ('succeeded', 'failed', 'canceled')
ORDER BY e.id;

-- name: PurgeWebhookPayloads :execrows
-- Retenção dos webhooks (D-12): esvazia o corpo dos eventos processados antes do corte; a linha
-- fica, para o dedup por event_id continuar valendo.
UPDATE webhook_events SET payload = '{}'::jsonb
WHERE processed_at IS NOT NULL AND processed_at < sqlc.arg(before)::timestamptz AND payload <> '{}'::jsonb;

-- name: ExecutionsUnderWorktree :one
-- Há execução com worktree sob o diretório dado (instalação de antes da ADR 0056 nos diretórios
-- compartilhados de ~/.cache/sentinela).
SELECT EXISTS (SELECT 1 FROM executions WHERE worktree LIKE sqlc.arg(prefix)::text || '%')::bool;
