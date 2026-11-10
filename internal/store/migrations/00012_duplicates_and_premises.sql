-- +goose Up
-- Duplicata humana, achado com premissa de produto e rejulgamento (ADR 0020).

-- Issue marcada como Duplicate no Linear: merge humano para a original, não recusa.
ALTER TABLE outcomes DROP CONSTRAINT outcomes_kind_check;
ALTER TABLE outcomes ADD CONSTRAINT outcomes_kind_check
    CHECK (kind IN ('accepted', 'rejected', 'merged', 'closed', 'reverted', 'duplicate_of'));
-- O achado duplicado acompanha o estado do achado original.
ALTER TABLE findings ADD COLUMN merged_into BIGINT REFERENCES findings (id) ON DELETE SET NULL;
CREATE INDEX findings_merged_into_idx ON findings (merged_into) WHERE merged_into IS NOT NULL;

-- Premissa de produto: o achado vira pergunta ao time até a resposta.
ALTER TABLE findings
    ADD COLUMN premise        TEXT,
    ADD COLUMN premise_answer TEXT CHECK (premise_answer IS NULL OR premise_answer IN ('intentional', 'not_intentional'));

-- Rodada de rejulgamento (sentinela rejudge): só mede, não analisa o repo nem publica.
ALTER TABLE analysis_runs DROP CONSTRAINT analysis_runs_mode_check;
ALTER TABLE analysis_runs ADD CONSTRAINT analysis_runs_mode_check
    CHECK (mode IN ('full', 'incremental', 'review_note', 'rejudge'));

-- +goose Down
DELETE FROM analysis_runs WHERE mode = 'rejudge';
ALTER TABLE analysis_runs DROP CONSTRAINT analysis_runs_mode_check;
ALTER TABLE analysis_runs ADD CONSTRAINT analysis_runs_mode_check CHECK (mode IN ('full', 'incremental', 'review_note'));
ALTER TABLE findings DROP COLUMN premise_answer, DROP COLUMN premise;
DROP INDEX findings_merged_into_idx;
ALTER TABLE findings DROP COLUMN merged_into;
DELETE FROM outcomes WHERE kind = 'duplicate_of';
ALTER TABLE outcomes DROP CONSTRAINT outcomes_kind_check;
ALTER TABLE outcomes ADD CONSTRAINT outcomes_kind_check
    CHECK (kind IN ('accepted', 'rejected', 'merged', 'closed', 'reverted'));
