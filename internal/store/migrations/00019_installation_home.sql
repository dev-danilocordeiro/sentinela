-- +goose Up
-- Diretório da instalação dona deste banco (D-06 da revisão de prontidão, ADR 0052). O init, o
-- serve e o worker recusam um banco registrado por outro diretório: um clone novo apontado para o
-- Postgres de outra instalação (mesma porta, mesmo projeto do compose) gravaria no histórico dela.
ALTER TABLE installation ADD COLUMN home_dir TEXT;

-- +goose Down
ALTER TABLE installation DROP COLUMN home_dir;
