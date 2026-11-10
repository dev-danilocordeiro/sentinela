-- +goose Up
-- Fila consciente de conflitos (ADR 0031): por que um job na fila ainda não começou (arquivos em
-- comum com um job anterior ainda em andamento ou com PR aberto). NULL quando não espera nada.
ALTER TABLE executions ADD COLUMN wait_reason TEXT;

-- +goose Down
ALTER TABLE executions DROP COLUMN wait_reason;
