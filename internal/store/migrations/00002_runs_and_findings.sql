-- +goose Up
CREATE TABLE analysis_runs (
    id            BIGSERIAL PRIMARY KEY,
    repo          TEXT        NOT NULL,
    base_sha      TEXT,
    head_sha      TEXT,
    status        TEXT        NOT NULL DEFAULT 'running'
                  CHECK (status IN ('running', 'succeeded', 'failed', 'aborted_budget')),
    budget_usd    NUMERIC(18, 9) NOT NULL,
    cost_usd      NUMERIC(18, 9) NOT NULL DEFAULT 0,
    error         TEXT,
    started_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at   TIMESTAMPTZ
);
CREATE INDEX analysis_runs_repo_started_idx ON analysis_runs (repo, started_at DESC);

CREATE TABLE findings (
    id                  BIGSERIAL PRIMARY KEY,
    run_id              BIGINT      REFERENCES analysis_runs (id) ON DELETE SET NULL,
    fingerprint         TEXT        NOT NULL UNIQUE,
    source              TEXT        NOT NULL CHECK (source IN ('code_analysis', 'sentry', 'datadog')),
    repo                TEXT        NOT NULL,
    module              TEXT        NOT NULL DEFAULT '',
    owners              TEXT[]      NOT NULL DEFAULT '{}',
    routing             JSONB       NOT NULL DEFAULT '{}',
    category            TEXT        NOT NULL CHECK (category IN ('bug', 'performance', 'improvement', 'security')),
    severity            TEXT        NOT NULL CHECK (severity IN ('critical', 'high', 'medium', 'low')),
    confidence          REAL        NOT NULL CHECK (confidence BETWEEN 0 AND 1),
    title               TEXT        NOT NULL,
    context             TEXT        NOT NULL DEFAULT '',
    evidence            JSONB       NOT NULL DEFAULT '[]',
    signals             JSONB       NOT NULL DEFAULT '[]',
    proposed_fix        TEXT        NOT NULL DEFAULT '',
    acceptance_criteria TEXT[]      NOT NULL DEFAULT '{}',
    scope_paths         TEXT[]      NOT NULL DEFAULT '{}',
    embedding           vector(1024),
    state               TEXT        NOT NULL DEFAULT 'open'
                        CHECK (state IN ('open', 'rejected', 'executing', 'fixed', 'reverted', 'stale')),
    resolution_reason   TEXT,
    linear_issue_id     TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX findings_repo_state_idx ON findings (repo, state);
CREATE INDEX findings_embedding_idx ON findings USING hnsw (embedding vector_cosine_ops);

CREATE TABLE suppressions (
    id           BIGSERIAL PRIMARY KEY,
    repo         TEXT        NOT NULL,
    module       TEXT        NOT NULL DEFAULT '',
    category     TEXT,
    fingerprint  TEXT,
    pattern      TEXT,
    reason       TEXT        NOT NULL,
    finding_id   BIGINT      REFERENCES findings (id) ON DELETE SET NULL,
    expires_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (fingerprint IS NOT NULL OR pattern IS NOT NULL)
);
CREATE INDEX suppressions_repo_idx ON suppressions (repo, module);

-- +goose Down
DROP TABLE suppressions;
DROP TABLE findings;
DROP TABLE analysis_runs;
