-- +goose Up
-- SQLite edition of postgres/00002_gateway.sql. Request logs are a single table
-- (no partitions); retention deletes old rows.

CREATE TABLE channels (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT,
    owner_id      TEXT NOT NULL REFERENCES users (id),
    name          TEXT NOT NULL,
    type          TEXT NOT NULL CHECK (type IN ('openai', 'anthropic')),
    base_url      TEXT NOT NULL,
    config        TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(config)),
    scope         TEXT NOT NULL DEFAULT 'private' CHECK (scope IN ('private', 'shared', 'global')),
    status        TEXT NOT NULL DEFAULT 'enabled' CHECK (status IN ('enabled', 'disabled')),
    priority      INTEGER NOT NULL DEFAULT 0,
    weight        INTEGER NOT NULL DEFAULT 1 CHECK (weight BETWEEN 1 AND 1000),
    version       INTEGER NOT NULL DEFAULT 1,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now'))
) STRICT;
CREATE INDEX channels_owner_idx ON channels (owner_id);
CREATE INDEX channels_scope_idx ON channels (scope) WHERE scope <> 'private';

CREATE TABLE channel_secrets (
    channel_id  TEXT NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    field       TEXT NOT NULL,
    ciphertext  TEXT NOT NULL,
    hint        TEXT,
    updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    PRIMARY KEY (channel_id, field)
) STRICT;

CREATE TABLE channel_shares (
    channel_id  TEXT NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    user_id     TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    PRIMARY KEY (channel_id, user_id)
) STRICT;
CREATE INDEX channel_shares_user_idx ON channel_shares (user_id);

CREATE TABLE channel_models (
    channel_id      TEXT NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    model           TEXT NOT NULL,
    upstream_model  TEXT NOT NULL,
    position        INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (channel_id, model)
) STRICT;
CREATE INDEX channel_models_model_idx ON channel_models (model);

CREATE TABLE prices (
    id                 TEXT PRIMARY KEY,
    kind               TEXT NOT NULL CHECK (kind IN ('sell', 'cost')),
    model              TEXT NOT NULL,
    channel_id         TEXT REFERENCES channels (id) ON DELETE CASCADE,
    input_per_m        INTEGER NOT NULL DEFAULT 0 CHECK (input_per_m >= 0),
    output_per_m       INTEGER NOT NULL DEFAULT 0 CHECK (output_per_m >= 0),
    cache_read_per_m   INTEGER NOT NULL DEFAULT 0 CHECK (cache_read_per_m >= 0),
    cache_write_per_m  INTEGER NOT NULL DEFAULT 0 CHECK (cache_write_per_m >= 0),
    per_request        INTEGER NOT NULL DEFAULT 0 CHECK (per_request >= 0),
    effective_at       TEXT NOT NULL,
    created_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    created_by         TEXT,
    CHECK ((kind = 'sell') = (channel_id IS NULL))
) STRICT;
CREATE INDEX prices_lookup_idx ON prices (kind, model, channel_id, effective_at DESC);

CREATE TABLE gateway_keys (
    id             TEXT PRIMARY KEY,
    workspace_id   TEXT,
    user_id        TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    prefix         TEXT NOT NULL,
    key_hash       BLOB NOT NULL UNIQUE,
    status         TEXT NOT NULL DEFAULT 'enabled' CHECK (status IN ('enabled', 'disabled', 'revoked')),
    policy         TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(policy)),
    expires_at     TEXT,
    last_used_at   TEXT,
    rotated_from   TEXT,
    version        INTEGER NOT NULL DEFAULT 1,
    created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    revoked_at     TEXT
) STRICT;
CREATE INDEX gateway_keys_user_idx ON gateway_keys (user_id) WHERE status <> 'revoked';

CREATE TABLE request_logs (
    id                   TEXT NOT NULL,
    started_at           TEXT NOT NULL,
    request_id           TEXT NOT NULL,
    user_id              TEXT,
    key_id               TEXT,
    key_name             TEXT,
    inbound              TEXT NOT NULL,
    model                TEXT NOT NULL DEFAULT '',
    channel_id           TEXT,
    channel_name         TEXT,
    upstream_model       TEXT,
    stream               INTEGER NOT NULL DEFAULT 0,
    status_code          INTEGER NOT NULL,
    error_class          TEXT,
    error_message        TEXT,
    attempts             INTEGER NOT NULL DEFAULT 0,
    fallback_path        TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(fallback_path)),
    ttft_ms              INTEGER,
    duration_ms          INTEGER NOT NULL,
    input_tokens         INTEGER NOT NULL DEFAULT 0,
    output_tokens        INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens    INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens   INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens     INTEGER NOT NULL DEFAULT 0,
    usage_estimated      INTEGER NOT NULL DEFAULT 0,
    cost_nano            INTEGER NOT NULL DEFAULT 0,
    charge_nano          INTEGER NOT NULL DEFAULT 0,
    sell_price_id        TEXT,
    cost_price_id        TEXT,
    ip_prefix            TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (id, started_at)
) STRICT;
CREATE INDEX request_logs_started_idx ON request_logs (started_at DESC);
CREATE INDEX request_logs_user_idx ON request_logs (user_id, started_at DESC);
CREATE INDEX request_logs_request_id_idx ON request_logs (request_id);

-- +goose Down
DROP TABLE request_logs;
DROP TABLE gateway_keys;
DROP TABLE prices;
DROP TABLE channel_models;
DROP TABLE channel_shares;
DROP TABLE channel_secrets;
DROP TABLE channels;
