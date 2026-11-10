-- +goose Up
-- Identidade da instalação (ADR 0047): o marcador das issues publicadas leva este id, e a
-- busca por marcador só reaproveita issue desta instalação. Uma linha só. legacy_markers_until é o
-- instante até o qual o marcador antigo, sem id, ainda é reconhecido: preenchido só quando a
-- instalação já tinha publicado issues antes desta migration (vazio numa instalação nova).
CREATE TABLE installation (
    singleton            BOOLEAN PRIMARY KEY DEFAULT true CHECK (singleton),
    id                   TEXT NOT NULL CHECK (id ~ '^[0-9a-f]{8}$'),
    legacy_markers_until TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO installation (id, legacy_markers_until)
SELECT substr(md5(random()::text || clock_timestamp()::text), 1, 8),
       CASE WHEN EXISTS (SELECT 1 FROM linear_issues) THEN now() END;

-- Desligamento de um achado da issue a que ficou ligado por engano (sentinela finding unlink): o
-- achado volta a pendente de publicação e o registro guarda de que issue saiu e por quê.
CREATE TABLE finding_unlinks (
    id          BIGSERIAL PRIMARY KEY,
    finding_id  BIGINT NOT NULL REFERENCES findings(id) ON DELETE CASCADE,
    linear_id   TEXT NOT NULL,
    identifier  TEXT NOT NULL,
    url         TEXT NOT NULL,
    role        TEXT NOT NULL,
    reason      TEXT NOT NULL CHECK (reason <> ''),
    actor       TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE finding_unlinks;
DROP TABLE installation;
