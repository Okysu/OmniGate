-- +goose Up
-- Client detection (phase13-api.md §2): the stable id of the client that sent
-- the request (claude-code | codex | cherry-studio | … | unknown) and its
-- version when parseable. NULL on rows logged before detection existed (read
-- as unknown). The raw User-Agent and other request headers are never stored.
ALTER TABLE request_logs ADD COLUMN client text;
ALTER TABLE request_logs ADD COLUMN client_version text;

-- +goose Down
ALTER TABLE request_logs DROP COLUMN client_version;
ALTER TABLE request_logs DROP COLUMN client;
