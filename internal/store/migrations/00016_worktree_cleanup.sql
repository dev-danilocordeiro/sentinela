-- +goose Up
-- Limpeza automática de worktrees (ADR 0037): quando o worktree da execução foi removido e
-- quanto disco liberou (só na execução mais recente do caminho; as outras ficam com NULL).
ALTER TABLE executions ADD COLUMN worktree_removed_at TIMESTAMPTZ;
ALTER TABLE executions ADD COLUMN worktree_freed_bytes BIGINT;

-- +goose Down
ALTER TABLE executions DROP COLUMN worktree_freed_bytes;
ALTER TABLE executions DROP COLUMN worktree_removed_at;
