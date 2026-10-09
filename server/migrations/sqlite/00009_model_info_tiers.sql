-- +goose Up
-- SQLite edition of postgres/00009_model_info_tiers.sql.

CREATE TABLE model_info (
    model           TEXT PRIMARY KEY,
    display_name    TEXT NOT NULL DEFAULT '',
    description     TEXT NOT NULL DEFAULT '',
    vendor          TEXT NOT NULL DEFAULT '',
    tags            TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(tags)),
    context_window  INTEGER,
    max_output      INTEGER,
    capabilities    TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(capabilities)),
    hidden          INTEGER NOT NULL DEFAULT 0,
    sort_order      INTEGER NOT NULL DEFAULT 0,
    version         INTEGER NOT NULL DEFAULT 1,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    updated_by      TEXT
) STRICT;

ALTER TABLE request_logs ADD COLUMN channel_tier TEXT
    CHECK (channel_tier IN ('own', 'shared', 'platform'));

-- +goose Down
ALTER TABLE request_logs DROP COLUMN channel_tier;
DROP TABLE model_info;
