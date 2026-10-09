-- +goose Up
-- SQLite edition of postgres/00016_settlement_dedupe.sql (ADR-0010).
CREATE TABLE settled_requests (
    request_id  TEXT PRIMARY KEY,
    settled_at  TEXT NOT NULL
) STRICT;
CREATE INDEX settled_requests_settled_idx ON settled_requests (settled_at);

-- +goose Down
DROP TABLE settled_requests;
