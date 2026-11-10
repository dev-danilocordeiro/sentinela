-- +goose Up
-- Fase 4: linha de execução (ADR 0018). executions já existia como fila (00003); aqui ganha o
-- que o worker, o gate, o shepherd e os comandos de humano precisam.
ALTER TABLE executions
    ADD COLUMN kind              TEXT        NOT NULL DEFAULT 'fix'
        CHECK (kind IN ('fix', 'rework')),
    ADD COLUMN repo              TEXT        NOT NULL DEFAULT '',
    ADD COLUMN issue_identifier  TEXT        NOT NULL DEFAULT '',
    ADD COLUMN linear_issue_id   TEXT        NOT NULL DEFAULT '',
    ADD COLUMN rework_note       TEXT,
    ADD COLUMN previous_id       BIGINT      REFERENCES executions (id) ON DELETE SET NULL,
    ADD COLUMN worktree          TEXT,
    ADD COLUMN job_dir           TEXT,
    ADD COLUMN session_id        TEXT,
    -- decisão da linha: approve | escalate (approver ou rodadas esgotadas) | blocked (builder
    -- bloqueado) | failed (contrato quebrado, erro)
    ADD COLUMN decision          TEXT
        CHECK (decision IS NULL OR decision IN ('approve', 'escalate', 'blocked', 'failed')),
    ADD COLUMN decision_reason   TEXT,
    ADD COLUMN rounds            INT         NOT NULL DEFAULT 0,
    -- gate: approve (passaria) | human (precisa de humano); a revisora só comenta enquanto
    -- github_apps.reviewer_approves for false
    ADD COLUMN gate              TEXT
        CHECK (gate IS NULL OR gate IN ('approve', 'human')),
    ADD COLUMN gate_reasons      JSONB       NOT NULL DEFAULT '[]',
    ADD COLUMN pr_url            TEXT,
    ADD COLUMN head_sha          TEXT,
    ADD COLUMN pr_state          TEXT
        CHECK (pr_state IS NULL OR pr_state IN ('open', 'merged', 'closed')),
    ADD COLUMN approved_by       TEXT,
    ADD COLUMN approved_at       TIMESTAMPTZ,
    ADD COLUMN shepherd_cycles   INT         NOT NULL DEFAULT 0,
    ADD COLUMN shepherd_handled  JSONB       NOT NULL DEFAULT '[]', -- eventos já tratados (sha/review)
    ADD COLUMN num_turns         INT         NOT NULL DEFAULT 0,
    ADD COLUMN artifacts         JSONB       NOT NULL DEFAULT '{}', -- primeira linha e resumo de cada arquivo
    ADD COLUMN started_at        TIMESTAMPTZ,
    ADD COLUMN finished_at       TIMESTAMPTZ;

-- Um registro por review de cada rodada: a métrica por lente sai daqui.
CREATE TABLE reviews (
    id           BIGSERIAL PRIMARY KEY,
    execution_id BIGINT      NOT NULL REFERENCES executions (id) ON DELETE CASCADE,
    round        INT         NOT NULL CHECK (round >= 1),
    lens         TEXT        NOT NULL CHECK (lens IN ('security', 'code', 'reliability', 'domain')),
    verdict      TEXT        NOT NULL CHECK (verdict IN ('PASS', 'CHANGES')),
    findings     JSONB       NOT NULL DEFAULT '[]',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (execution_id, round, lens)
);
CREATE INDEX reviews_lens_idx ON reviews (lens, verdict);

-- +goose Down
DROP TABLE reviews;
ALTER TABLE executions
    DROP COLUMN finished_at, DROP COLUMN started_at, DROP COLUMN artifacts, DROP COLUMN num_turns,
    DROP COLUMN shepherd_handled, DROP COLUMN shepherd_cycles, DROP COLUMN approved_at,
    DROP COLUMN approved_by, DROP COLUMN pr_state, DROP COLUMN head_sha, DROP COLUMN pr_url,
    DROP COLUMN gate_reasons, DROP COLUMN gate, DROP COLUMN rounds, DROP COLUMN decision_reason,
    DROP COLUMN decision, DROP COLUMN session_id, DROP COLUMN job_dir, DROP COLUMN worktree,
    DROP COLUMN previous_id, DROP COLUMN rework_note, DROP COLUMN linear_issue_id,
    DROP COLUMN issue_identifier, DROP COLUMN repo, DROP COLUMN kind;
