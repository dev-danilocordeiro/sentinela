-- +goose Up
-- Confirmação humana de um exemplo do Judge: quem revisou o exemplo gerado pelo Curator e o
-- assumiu como decisão própria, e quando. Exemplo confirmado não é desativado pela curadoria.
ALTER TABLE feedback_examples
    ADD COLUMN confirmed_by      TEXT,
    ADD COLUMN confirmed_at      TIMESTAMPTZ,
    ADD COLUMN confirmation_note TEXT,
    ADD CONSTRAINT feedback_examples_confirmation_check
        CHECK ((confirmed_by IS NULL) = (confirmed_at IS NULL));

-- +goose Down
ALTER TABLE feedback_examples
    DROP CONSTRAINT feedback_examples_confirmation_check,
    DROP COLUMN confirmation_note,
    DROP COLUMN confirmed_at,
    DROP COLUMN confirmed_by;
