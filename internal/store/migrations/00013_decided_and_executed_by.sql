-- +goose Up
-- Quem decidiu e quem executou um desfecho são coisas diferentes (ADR 0022). decided_by: quem
-- tomou a decisão (deveria ser sempre humano, princípio 6). executed_by: quem fez a ação no
-- Linear ou no GitHub (uma pessoa, ou um agente a pedido dela). Métricas, Curator e supressões
-- usam decided_by: decisão humana executada por agente conta normalmente.
ALTER TABLE outcomes
    ADD COLUMN decided_by  TEXT NOT NULL DEFAULT 'human' CHECK (decided_by IN ('human', 'agent')),
    ADD COLUMN executed_by TEXT NOT NULL DEFAULT 'human' CHECK (executed_by IN ('human', 'agent'));
-- performed_by = 'agent_on_behalf' era decisão humana executada por agente.
UPDATE outcomes SET decided_by = 'human', executed_by = 'agent' WHERE performed_by = 'agent_on_behalf';
ALTER TABLE outcomes DROP COLUMN performed_by;

-- +goose Down
ALTER TABLE outcomes ADD COLUMN performed_by TEXT NOT NULL DEFAULT 'human'
    CHECK (performed_by IN ('human', 'agent_on_behalf'));
UPDATE outcomes SET performed_by = 'agent_on_behalf' WHERE executed_by = 'agent' OR decided_by = 'agent';
ALTER TABLE outcomes DROP COLUMN executed_by, DROP COLUMN decided_by;
