-- name: FindingByFingerprint :one
SELECT id, state, title FROM findings WHERE fingerprint = $1;

-- name: SimilarFindings :many
-- Achados do mesmo repo mais próximos por cosseno (pgvector), para dedup e para o juiz. Com um
-- vetor nulo o cosseno é NaN, que quebraria o JSON do Judge: vira 0 (sem semelhança).
SELECT id, fingerprint, title, category, state, module,
       COALESCE(NULLIF((1 - (embedding <=> sqlc.arg(embedding)::vector))::float8, 'NaN'::float8), 0)::float8 AS similarity
FROM findings
WHERE repo = sqlc.arg(repo) AND embedding IS NOT NULL
ORDER BY embedding <=> sqlc.arg(embedding)::vector
LIMIT sqlc.arg(max_results);

-- name: JudgeExamples :many
-- Exemplos decididos por humanos no repo: aceitos (fixed) e recusados (rejected).
SELECT title, category, state, COALESCE(resolution_reason, '')::text AS reason
FROM findings
WHERE repo = $1 AND state IN ('fixed', 'rejected') AND merged_into IS NULL -- duplicata humana não é exemplo próprio
  AND NOT EXISTS (SELECT 1 FROM outcomes o WHERE o.finding_id = findings.id AND o.decided_by = 'agent')
ORDER BY updated_at DESC
LIMIT $2;

-- name: ActiveSuppressions :many
-- Supressões valem pela decisão humana: a de um achado cuja recusa foi decidida por agente
-- (decided_by = 'agent', violação do princípio 6) não barra nada.
SELECT s.* FROM suppressions s
WHERE s.repo = $1 AND (s.expires_at IS NULL OR s.expires_at > now())
  AND NOT EXISTS (
      SELECT 1 FROM outcomes o
      WHERE o.finding_id = s.finding_id AND o.kind = 'rejected' AND o.decided_by = 'agent'
  );

-- name: InsertFinding :one
INSERT INTO findings (
    run_id, fingerprint, source, repo, module, owners, routing, category, severity,
    confidence, title, context, evidence, signals, proposed_fix, acceptance_criteria,
    scope_paths, embedding, related_modules, judge_confidence, judge_reason, origin_execution_id, premise
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23
)
ON CONFLICT (fingerprint) DO NOTHING
RETURNING id;

-- name: ListFindingsByRun :many
SELECT id, fingerprint, source, repo, module, owners, routing, category, severity, confidence,
       title, context, evidence, signals, proposed_fix, acceptance_criteria, scope_paths,
       related_modules, judge_confidence, judge_reason, state, created_at
FROM findings
WHERE run_id = $1
ORDER BY CASE severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 ELSE 3 END, id;

-- name: OpenFindingsForOverlap :many
-- Achados abertos ou em execução do repo, com critérios e evidências, para o Judge reduzir o
-- escopo de um achado novo que se sobrepõe em parte a um deles.
SELECT f.id, f.fingerprint, f.title, f.category, f.state, f.acceptance_criteria, f.evidence,
       COALESCE((SELECT li.identifier FROM linear_issues li
                 WHERE li.finding_id = f.id AND li.role = 'primary' LIMIT 1), '')::text AS identifier
FROM findings f
WHERE f.repo = $1 AND f.state IN ('open', 'executing') AND f.merged_into IS NULL
ORDER BY f.id;

-- name: FindingPublication :one
-- Para decidir quem absorve quem numa mesclagem: idade do achado e da issue principal.
SELECT f.id, f.fingerprint, f.title,
       COALESCE(li.id, 0)::bigint AS issue_seq, COALESCE(li.identifier, '')::text AS identifier
FROM findings f
LEFT JOIN linear_issues li ON li.finding_id = f.id AND li.role = 'primary'
WHERE f.fingerprint = $1;

-- name: LocationCandidates :many
-- Achados do repo que podem ser o mesmo problema de um novo pelo lugar no código (ADR 0025):
-- abertos, em execução, recusados e duplicatas marcadas por humano, sem desfecho decidido por
-- agente. suppressed diz se há
-- supressão ativa (pelo achado ou pelo fingerprint) que vale pela decisão humana.
SELECT f.fingerprint, f.title, f.category, f.state, f.module, f.evidence, f.acceptance_criteria,
       (f.merged_into IS NOT NULL)::bool AS merged,
       EXISTS (
           SELECT 1 FROM suppressions s
           WHERE s.repo = f.repo AND (s.finding_id = f.id OR s.fingerprint = f.fingerprint)
             AND (s.expires_at IS NULL OR s.expires_at > now())
             AND NOT EXISTS (
                 SELECT 1 FROM outcomes o
                 WHERE o.finding_id = s.finding_id AND o.kind = 'rejected' AND o.decided_by = 'agent'
             )
       )::bool AS suppressed,
       COALESCE((SELECT li.identifier FROM linear_issues li
                 WHERE li.finding_id = f.id AND li.role = 'primary' LIMIT 1), '')::text AS identifier
FROM findings f
WHERE f.repo = $1 AND (f.state IN ('open', 'executing', 'rejected') OR f.merged_into IS NOT NULL)
  -- recusa decidida por agente não é decisão humana (princípio 6)
  AND NOT EXISTS (SELECT 1 FROM outcomes o WHERE o.finding_id = f.id AND o.decided_by = 'agent')
ORDER BY f.id;

-- name: MergedFindings :many
-- Duplicatas humanas do repo: cada achado e aquele em que foi mesclado (merged_into).
SELECT f.fingerprint, m.fingerprint AS merged_into
FROM findings f JOIN findings m ON m.id = f.merged_into
WHERE f.repo = $1;

-- name: RefreshFindingText :execrows
-- Regenera o texto de um achado ainda sem issue (ADR 0036): só conteúdo. Estado, roteamento,
-- severidade, veredito e confiança do Judge e fingerprint ficam como foram decididos.
UPDATE findings SET module = $2, title = $3, context = $4, evidence = $5, signals = $6,
    proposed_fix = $7, acceptance_criteria = $8, scope_paths = $9, embedding = $10, updated_at = now()
WHERE findings.id = $1 AND findings.state = 'open' AND findings.merged_into IS NULL
  AND NOT EXISTS (SELECT 1 FROM linear_issues li WHERE li.finding_id = findings.id);
