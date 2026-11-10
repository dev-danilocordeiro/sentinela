-- +goose Up
-- Gatilho recusado (ADR 0042): a label de gatilho aplicada por quem fix_trigger.allowed não
-- permite. Uma linha por entrada do histórico da issue (event_id): o Sentinela comenta uma vez e
-- ignora até alguém autorizado aplicar a label de novo. A label e a issue não são mexidas.
CREATE TABLE trigger_refusals (
    id              BIGSERIAL PRIMARY KEY,
    finding_id      BIGINT NOT NULL REFERENCES findings(id) ON DELETE CASCADE,
    linear_issue_id TEXT NOT NULL,
    event_id        TEXT NOT NULL UNIQUE,
    actor           TEXT NOT NULL,
    allowed         TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE trigger_refusals;
