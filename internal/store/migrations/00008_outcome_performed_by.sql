-- +goose Up
-- Quem executou a ação que gerou o desfecho: a pessoa no Linear/GitHub ('human') ou um agente a
-- pedido explícito do usuário ('agent_on_behalf'). Desfechos executados por agente ficam fora das
-- métricas de decisão humana (princípio 6). note explica o caso.
ALTER TABLE outcomes
    ADD COLUMN performed_by TEXT NOT NULL DEFAULT 'human'
        CHECK (performed_by IN ('human', 'agent_on_behalf')),
    ADD COLUMN note TEXT;

-- +goose Down
ALTER TABLE outcomes DROP COLUMN note, DROP COLUMN performed_by;
