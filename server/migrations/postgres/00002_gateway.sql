-- +goose Up
-- Round 2 (Phase 1): channels, model mapping, versioned prices, gateway keys, request logs.

CREATE TABLE channels (
    id            uuid PRIMARY KEY,
    workspace_id  uuid,
    owner_id      uuid NOT NULL REFERENCES users (id),
    name          text NOT NULL,
    type          text NOT NULL CHECK (type IN ('openai', 'anthropic')),
    base_url      text NOT NULL,
    config        jsonb NOT NULL DEFAULT '{}'::jsonb,
    scope         text NOT NULL DEFAULT 'private' CHECK (scope IN ('private', 'shared', 'global')),
    status        text NOT NULL DEFAULT 'enabled' CHECK (status IN ('enabled', 'disabled')),
    priority      integer NOT NULL DEFAULT 0,
    weight        integer NOT NULL DEFAULT 1 CHECK (weight BETWEEN 1 AND 1000),
    version       integer NOT NULL DEFAULT 1,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX channels_owner_idx ON channels (owner_id);
CREATE INDEX channels_scope_idx ON channels (scope) WHERE scope <> 'private';

-- Encrypted credential fields (ADR-0008). AAD = channel_secret:<channel_id>:<field>.
CREATE TABLE channel_secrets (
    channel_id  uuid NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    field       text NOT NULL,
    ciphertext  text NOT NULL,
    hint        text,                 -- last 4 characters, for display only
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, field)
);

CREATE TABLE channel_shares (
    channel_id  uuid NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, user_id)
);
CREATE INDEX channel_shares_user_idx ON channel_shares (user_id);

-- Logical model name -> upstream model name, per channel.
CREATE TABLE channel_models (
    channel_id      uuid NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    model           text NOT NULL,
    upstream_model  text NOT NULL,
    position        integer NOT NULL DEFAULT 0,
    PRIMARY KEY (channel_id, model)
);
CREATE INDEX channel_models_model_idx ON channel_models (model);

-- Versioned prices. Immutable rows: a price change is a new row (ADR-0006/0007).
-- kind='sell': per logical model (channel_id NULL); kind='cost': per channel + upstream model.
CREATE TABLE prices (
    id                 uuid PRIMARY KEY,
    kind               text NOT NULL CHECK (kind IN ('sell', 'cost')),
    model              text NOT NULL,
    channel_id         uuid REFERENCES channels (id) ON DELETE CASCADE,
    input_per_m        bigint NOT NULL DEFAULT 0 CHECK (input_per_m >= 0),
    output_per_m       bigint NOT NULL DEFAULT 0 CHECK (output_per_m >= 0),
    cache_read_per_m   bigint NOT NULL DEFAULT 0 CHECK (cache_read_per_m >= 0),
    cache_write_per_m  bigint NOT NULL DEFAULT 0 CHECK (cache_write_per_m >= 0),
    per_request        bigint NOT NULL DEFAULT 0 CHECK (per_request >= 0),
    effective_at       timestamptz NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    created_by         uuid,
    CHECK ((kind = 'sell') = (channel_id IS NULL))
);
CREATE INDEX prices_lookup_idx ON prices (kind, model, channel_id, effective_at DESC);

CREATE TABLE gateway_keys (
    id             uuid PRIMARY KEY,
    workspace_id   uuid,
    user_id        uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name           text NOT NULL,
    prefix         text NOT NULL,
    key_hash       bytea NOT NULL UNIQUE,      -- SHA-256 of the full key (ADR-0008)
    status         text NOT NULL DEFAULT 'enabled' CHECK (status IN ('enabled', 'disabled', 'revoked')),
    policy         jsonb NOT NULL DEFAULT '{}'::jsonb,
    expires_at     timestamptz,
    last_used_at   timestamptz,
    rotated_from   uuid,
    version        integer NOT NULL DEFAULT 1,
    created_at     timestamptz NOT NULL DEFAULT now(),
    revoked_at     timestamptz
);
CREATE INDEX gateway_keys_user_idx ON gateway_keys (user_id) WHERE status <> 'revoked';

-- Request logs: metadata only, never prompt/response bodies. Monthly range
-- partitions are created ahead of time by the app; the DEFAULT partition only
-- catches stragglers.
CREATE TABLE request_logs (
    id                   uuid NOT NULL,
    started_at           timestamptz NOT NULL,
    request_id           text NOT NULL,
    user_id              uuid,
    key_id               uuid,
    key_name             text,
    inbound              text NOT NULL,
    model                text NOT NULL DEFAULT '',
    channel_id           uuid,
    channel_name         text,
    upstream_model       text,
    stream               boolean NOT NULL DEFAULT false,
    status_code          integer NOT NULL,
    error_class          text,
    error_message        text,
    attempts             integer NOT NULL DEFAULT 0,
    fallback_path        jsonb NOT NULL DEFAULT '[]'::jsonb,
    ttft_ms              integer,
    duration_ms          integer NOT NULL,
    input_tokens         bigint NOT NULL DEFAULT 0,
    output_tokens        bigint NOT NULL DEFAULT 0,
    cache_read_tokens    bigint NOT NULL DEFAULT 0,
    cache_write_tokens   bigint NOT NULL DEFAULT 0,
    reasoning_tokens     bigint NOT NULL DEFAULT 0,
    usage_estimated      boolean NOT NULL DEFAULT false,
    cost_nano            bigint NOT NULL DEFAULT 0,
    charge_nano          bigint NOT NULL DEFAULT 0,
    sell_price_id        uuid,
    cost_price_id        uuid,
    ip_prefix            text NOT NULL DEFAULT '',
    PRIMARY KEY (id, started_at)
) PARTITION BY RANGE (started_at);
CREATE TABLE request_logs_default PARTITION OF request_logs DEFAULT;
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
