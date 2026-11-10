-- +goose Up
-- Fase 3: issues publicadas no Linear, exemplos few-shot do Curator e limiares calibrados.

-- Uma linha por issue criada para um achado: a principal, no time dono, e as relacionadas,
-- nos boards dos outros times afetados (achados entre módulos).
CREATE TABLE linear_issues (
    id          BIGSERIAL PRIMARY KEY,
    finding_id  BIGINT      NOT NULL REFERENCES findings (id) ON DELETE CASCADE,
    run_id      BIGINT      REFERENCES analysis_runs (id) ON DELETE SET NULL,
    role        TEXT        NOT NULL CHECK (role IN ('primary', 'related')),
    team        TEXT        NOT NULL,             -- time de engenharia do sentinela.yaml
    module      TEXT        NOT NULL DEFAULT '',
    linear_team TEXT        NOT NULL,             -- chave do time no Linear (ex.: EX)
    linear_id   TEXT        NOT NULL UNIQUE,
    identifier  TEXT        NOT NULL,             -- ex.: EX-42
    url         TEXT        NOT NULL,
    state_name  TEXT        NOT NULL DEFAULT '',
    state_type  TEXT        NOT NULL DEFAULT '',  -- triage | backlog | unstarted | started | completed | canceled | duplicate
    synced_at   TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (finding_id, role, team)
);
CREATE INDEX linear_issues_run_idx ON linear_issues (run_id);

-- Exemplos aceitos e recusados escolhidos pelo Feedback Curator para o Judge.
CREATE TABLE feedback_examples (
    id          BIGSERIAL PRIMARY KEY,
    repo        TEXT        NOT NULL,
    module      TEXT        NOT NULL DEFAULT '',
    category    TEXT        NOT NULL,
    finding_id  BIGINT      REFERENCES findings (id) ON DELETE CASCADE,
    outcome     TEXT        NOT NULL CHECK (outcome IN ('accepted', 'rejected')),
    title       TEXT        NOT NULL,
    lesson      TEXT        NOT NULL,
    active      BOOLEAN     NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX feedback_examples_repo_idx ON feedback_examples (repo) WHERE active;

-- min_confidence calibrado por repo, módulo ('' = repo inteiro) e categoria.
CREATE TABLE thresholds (
    repo           TEXT        NOT NULL,
    module         TEXT        NOT NULL DEFAULT '',
    category       TEXT        NOT NULL,
    min_confidence REAL        NOT NULL CHECK (min_confidence BETWEEN 0 AND 1),
    reason         TEXT        NOT NULL,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (repo, module, category)
);

-- +goose Down
DROP TABLE thresholds;
DROP TABLE feedback_examples;
DROP TABLE linear_issues;
