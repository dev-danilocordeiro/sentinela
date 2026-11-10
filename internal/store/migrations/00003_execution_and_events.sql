-- +goose Up
CREATE TABLE executions (
    id            BIGSERIAL PRIMARY KEY,
    finding_id    BIGINT      NOT NULL REFERENCES findings (id) ON DELETE CASCADE,
    status        TEXT        NOT NULL DEFAULT 'queued'
                  CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'canceled')),
    attempt       INT         NOT NULL DEFAULT 0,
    model         TEXT,
    branch        TEXT,
    pr_number     INT,
    cost_usd      NUMERIC(18, 9) NOT NULL DEFAULT 0,
    error         TEXT,
    run_after     TIMESTAMPTZ NOT NULL DEFAULT now(),
    locked_at     TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Consumido com FOR UPDATE SKIP LOCKED pelo worker (Fase 4).
CREATE INDEX executions_queue_idx ON executions (run_after) WHERE status = 'queued';
-- No máximo uma execução ativa por achado.
CREATE UNIQUE INDEX executions_one_active_idx ON executions (finding_id) WHERE status IN ('queued', 'running');

CREATE TABLE webhook_events (
    id           BIGSERIAL PRIMARY KEY,
    provider     TEXT        NOT NULL CHECK (provider IN ('linear', 'github', 'sentry', 'datadog')),
    event_id     TEXT        NOT NULL,
    event_type   TEXT        NOT NULL,
    payload      JSONB       NOT NULL,
    received_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at TIMESTAMPTZ,
    UNIQUE (provider, event_id)
);

CREATE TABLE outcomes (
    id           BIGSERIAL PRIMARY KEY,
    finding_id   BIGINT      NOT NULL REFERENCES findings (id) ON DELETE CASCADE,
    execution_id BIGINT      REFERENCES executions (id) ON DELETE SET NULL,
    kind         TEXT        NOT NULL
                 CHECK (kind IN ('accepted', 'rejected', 'merged', 'closed', 'reverted')),
    reason       TEXT,
    actor        TEXT,
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX outcomes_finding_idx ON outcomes (finding_id);

-- +goose Down
DROP TABLE outcomes;
DROP TABLE webhook_events;
DROP TABLE executions;
