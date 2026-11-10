-- +goose Up
-- Provider de LLM de cada chamada (ADR 0013). Chamadas pela assinatura do Claude Code
-- gravam custo 0; o que conta nelas é o número de chamadas.
ALTER TABLE llm_calls
    ADD COLUMN provider TEXT NOT NULL DEFAULT 'anthropic_api'
        CHECK (provider IN ('anthropic_api', 'claude_code', 'replay'));
ALTER TABLE llm_calls DROP CONSTRAINT llm_calls_mode_check;
ALTER TABLE llm_calls ADD CONSTRAINT llm_calls_mode_check CHECK (mode IN ('sync', 'batch', 'cli'));

-- +goose Down
DELETE FROM llm_calls WHERE mode = 'cli';
ALTER TABLE llm_calls DROP CONSTRAINT llm_calls_mode_check;
ALTER TABLE llm_calls ADD CONSTRAINT llm_calls_mode_check CHECK (mode IN ('sync', 'batch'));
ALTER TABLE llm_calls DROP COLUMN provider;
