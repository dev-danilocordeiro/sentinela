-- +goose Up
-- Retomar a sessão no retry (ADR 0030). session_log: onde o Claude Code gravou a sessão do job
-- ({"main": "<arquivo>.jsonl", "subagents": ["...jsonl"]}). resume_note: o que o retry fez com a
-- sessão da execução anterior (retomou, ou recomeçou do zero e por quê).
ALTER TABLE executions
    ADD COLUMN session_log JSONB,
    ADD COLUMN resume_note TEXT;

-- +goose Down
ALTER TABLE executions DROP COLUMN resume_note, DROP COLUMN session_log;
