-- name: InsertLLMCall :one
INSERT INTO llm_calls (
    run_id, agent, model, prompt_version, mode, batch_id, custom_id, message_id,
    status, stop_reason, input_tokens, output_tokens, cache_write_5m_tokens,
    cache_write_1h_tokens, cache_read_tokens, cost_usd, latency_ms, error, provider
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19
)
RETURNING id;

-- name: ListLLMCallsByRun :many
SELECT * FROM llm_calls WHERE run_id = $1 ORDER BY id;

-- name: UpsertPromptVersion :exec
INSERT INTO prompt_versions (agent, version, sha256)
VALUES ($1, $2, $3)
ON CONFLICT (agent, version) DO NOTHING;
