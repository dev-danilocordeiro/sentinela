-- +goose Up
CREATE TABLE prompt_versions (
    agent       TEXT        NOT NULL,
    version     TEXT        NOT NULL,
    sha256      TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (agent, version)
);

CREATE TABLE llm_calls (
    id                    BIGSERIAL PRIMARY KEY,
    run_id                BIGINT      REFERENCES analysis_runs (id) ON DELETE SET NULL,
    agent                 TEXT        NOT NULL,
    model                 TEXT        NOT NULL,
    prompt_version        TEXT        NOT NULL,
    mode                  TEXT        NOT NULL CHECK (mode IN ('sync', 'batch')),
    batch_id              TEXT,
    custom_id             TEXT,
    message_id            TEXT,
    status                TEXT        NOT NULL CHECK (status IN ('succeeded', 'errored', 'canceled', 'expired')),
    stop_reason           TEXT,
    input_tokens          BIGINT      NOT NULL DEFAULT 0,
    output_tokens         BIGINT      NOT NULL DEFAULT 0,
    cache_write_5m_tokens BIGINT      NOT NULL DEFAULT 0,
    cache_write_1h_tokens BIGINT      NOT NULL DEFAULT 0,
    cache_read_tokens     BIGINT      NOT NULL DEFAULT 0,
    cost_usd              NUMERIC(18, 9) NOT NULL,
    latency_ms            BIGINT      NOT NULL,
    error                 TEXT,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX llm_calls_run_idx ON llm_calls (run_id);
CREATE INDEX llm_calls_agent_created_idx ON llm_calls (agent, created_at);

-- +goose Down
DROP TABLE llm_calls;
DROP TABLE prompt_versions;
