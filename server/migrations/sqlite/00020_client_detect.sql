-- +goose Up
-- SQLite edition of postgres/00020_client_detect.sql (phase13-api.md §2).
ALTER TABLE request_logs ADD COLUMN client TEXT;
ALTER TABLE request_logs ADD COLUMN client_version TEXT;

-- +goose Down
ALTER TABLE request_logs DROP COLUMN client_version;
ALTER TABLE request_logs DROP COLUMN client;
