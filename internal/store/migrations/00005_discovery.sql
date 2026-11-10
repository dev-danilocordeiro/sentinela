-- +goose Up
-- Fase 2: modo da rodada, rodada sem mudanças, itens por agente e decisão do juiz.
ALTER TABLE analysis_runs DROP CONSTRAINT analysis_runs_status_check;
ALTER TABLE analysis_runs ADD CONSTRAINT analysis_runs_status_check
    CHECK (status IN ('running', 'succeeded', 'failed', 'aborted_budget', 'no_changes'));
ALTER TABLE analysis_runs
    ADD COLUMN mode  TEXT  NOT NULL DEFAULT 'full' CHECK (mode IN ('full', 'incremental')),
    ADD COLUMN stats JSONB NOT NULL DEFAULT '{}';

-- Um registro por item processado por um agente numa rodada: pacote triado, pacote
-- analisado, achado julgado. Itens pulados (recusa, erro, corte de contexto) guardam o motivo.
CREATE TABLE run_items (
    id         BIGSERIAL PRIMARY KEY,
    run_id     BIGINT      NOT NULL REFERENCES analysis_runs (id) ON DELETE CASCADE,
    agent      TEXT        NOT NULL,
    item_key   TEXT        NOT NULL,
    status     TEXT        NOT NULL CHECK (status IN ('ok', 'skipped', 'failed')),
    model      TEXT,
    output     JSONB,
    reason     TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (run_id, agent, item_key)
);

ALTER TABLE findings
    ADD COLUMN related_modules  TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN judge_confidence REAL CHECK (judge_confidence BETWEEN 0 AND 1),
    ADD COLUMN judge_reason     TEXT;

-- +goose Down
ALTER TABLE findings
    DROP COLUMN judge_reason,
    DROP COLUMN judge_confidence,
    DROP COLUMN related_modules;
DROP TABLE run_items;
ALTER TABLE analysis_runs DROP COLUMN stats, DROP COLUMN mode;
DELETE FROM analysis_runs WHERE status = 'no_changes';
ALTER TABLE analysis_runs DROP CONSTRAINT analysis_runs_status_check;
ALTER TABLE analysis_runs ADD CONSTRAINT analysis_runs_status_check
    CHECK (status IN ('running', 'succeeded', 'failed', 'aborted_budget'));
