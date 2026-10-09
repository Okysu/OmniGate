-- +goose Up
-- SQLite edition of postgres/00005_model_protocol.sql.
ALTER TABLE channel_models ADD COLUMN upstream_protocol TEXT NOT NULL DEFAULT ''
    CHECK (upstream_protocol IN ('', 'chat', 'responses'));

-- +goose Down
ALTER TABLE channel_models DROP COLUMN upstream_protocol;
