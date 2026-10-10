-- +goose Up
-- SQLite edition of postgres/00019_session_affinity.sql (phase12-api.md §4).
ALTER TABLE request_logs ADD COLUMN affinity TEXT;
ALTER TABLE request_logs ADD COLUMN affinity_rule TEXT;

-- +goose Down
ALTER TABLE request_logs DROP COLUMN affinity_rule;
ALTER TABLE request_logs DROP COLUMN affinity;
