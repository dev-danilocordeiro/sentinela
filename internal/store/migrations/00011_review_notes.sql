-- +goose Up
-- Notas dos reviewers fora do escopo viram achados candidatos (ADR 0019): fonte nova no contrato
-- Finding, ligação com a execução de origem e rodadas do tipo review_note.
ALTER TABLE findings DROP CONSTRAINT findings_source_check;
ALTER TABLE findings ADD CONSTRAINT findings_source_check
    CHECK (source IN ('code_analysis', 'sentry', 'datadog', 'review_note'));
ALTER TABLE findings ADD COLUMN origin_execution_id BIGINT REFERENCES executions (id) ON DELETE SET NULL;
ALTER TABLE analysis_runs DROP CONSTRAINT analysis_runs_mode_check;
ALTER TABLE analysis_runs ADD CONSTRAINT analysis_runs_mode_check
    CHECK (mode IN ('full', 'incremental', 'review_note'));

-- +goose Down
DELETE FROM findings WHERE source = 'review_note';
DELETE FROM analysis_runs WHERE mode = 'review_note';
ALTER TABLE analysis_runs DROP CONSTRAINT analysis_runs_mode_check;
ALTER TABLE analysis_runs ADD CONSTRAINT analysis_runs_mode_check CHECK (mode IN ('full', 'incremental'));
ALTER TABLE findings DROP COLUMN origin_execution_id;
ALTER TABLE findings DROP CONSTRAINT findings_source_check;
ALTER TABLE findings ADD CONSTRAINT findings_source_check CHECK (source IN ('code_analysis', 'sentry', 'datadog'));
