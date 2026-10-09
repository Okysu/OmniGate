-- +goose Up
-- SQLite edition of postgres/00003_billing.sql. Row locks (FOR UPDATE) do not
-- exist in SQLite: every transaction starts with BEGIN IMMEDIATE, which holds
-- the database write lock, so balance changes are serialized.

CREATE TABLE wallets (
    id             TEXT PRIMARY KEY,
    workspace_id   TEXT,
    user_id        TEXT NOT NULL UNIQUE REFERENCES users (id),
    balance_nano   INTEGER NOT NULL DEFAULT 0,
    reserved_nano  INTEGER NOT NULL DEFAULT 0 CHECK (reserved_nano >= 0),
    version        INTEGER NOT NULL DEFAULT 1,
    created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now'))
) STRICT;

CREATE TABLE ledger_entries (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT,
    wallet_id           TEXT NOT NULL REFERENCES wallets (id),
    kind                TEXT NOT NULL CHECK (kind IN ('grant', 'charge', 'refund', 'adjust')),
    amount_nano         INTEGER NOT NULL,
    balance_after_nano  INTEGER NOT NULL,
    ref_type            TEXT NOT NULL CONSTRAINT ledger_entries_ref_type_check CHECK (ref_type IN ('request', 'redeem', 'admin')),
    ref_id              TEXT NOT NULL,
    note                TEXT,
    created_by          TEXT,
    created_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now'))
) STRICT;
CREATE INDEX ledger_entries_wallet_idx ON ledger_entries (wallet_id, created_at DESC, id DESC);
CREATE UNIQUE INDEX ledger_entries_charge_ref_uidx ON ledger_entries (ref_type, ref_id) WHERE kind = 'charge';

CREATE TABLE reservations (
    request_id   TEXT PRIMARY KEY,
    wallet_id    TEXT NOT NULL REFERENCES wallets (id),
    amount_nano  INTEGER NOT NULL CHECK (amount_nano > 0),
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    expires_at   TEXT NOT NULL
) STRICT;
CREATE INDEX reservations_expires_idx ON reservations (expires_at);
CREATE INDEX reservations_wallet_idx ON reservations (wallet_id);

CREATE TABLE redeem_batches (
    id                        TEXT PRIMARY KEY,
    workspace_id              TEXT,
    kind                      TEXT NOT NULL CONSTRAINT redeem_batches_kind_check CHECK (kind IN ('wallet_credit')),
    amount_nano               INTEGER,
    payload                   TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(payload)),
    count                     INTEGER NOT NULL CHECK (count > 0),
    max_redemptions_per_code  INTEGER NOT NULL DEFAULT 1 CHECK (max_redemptions_per_code > 0),
    per_user_limit            INTEGER NOT NULL DEFAULT 1 CHECK (per_user_limit > 0),
    valid_from                TEXT,
    expires_at                TEXT,
    note                      TEXT,
    status                    TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_by                TEXT NOT NULL REFERENCES users (id),
    created_at                TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at                TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    version                   INTEGER NOT NULL DEFAULT 1,
    CONSTRAINT redeem_batches_check CHECK (kind <> 'wallet_credit' OR amount_nano > 0),
    CHECK (valid_from IS NULL OR expires_at IS NULL OR expires_at > valid_from)
) STRICT;
CREATE INDEX redeem_batches_created_idx ON redeem_batches (created_at DESC, id DESC);

CREATE TABLE redeem_codes (
    id          TEXT PRIMARY KEY,
    batch_id    TEXT NOT NULL REFERENCES redeem_batches (id),
    code_hash   BLOB NOT NULL UNIQUE,
    prefix      TEXT NOT NULL,
    used_count  INTEGER NOT NULL DEFAULT 0 CHECK (used_count >= 0),
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now'))
) STRICT;
CREATE INDEX redeem_codes_batch_idx ON redeem_codes (batch_id);

CREATE TABLE redemptions (
    id               TEXT PRIMARY KEY,
    workspace_id     TEXT,
    code_id          TEXT NOT NULL REFERENCES redeem_codes (id),
    batch_id         TEXT NOT NULL REFERENCES redeem_batches (id),
    user_id          TEXT NOT NULL REFERENCES users (id),
    ledger_entry_id  TEXT REFERENCES ledger_entries (id),
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now'))
) STRICT;
CREATE INDEX redemptions_batch_user_idx ON redemptions (batch_id, user_id);
CREATE INDEX redemptions_user_idx ON redemptions (user_id, created_at DESC);
CREATE INDEX redemptions_code_idx ON redemptions (code_id);

INSERT INTO system_settings (key, value) VALUES ('billing.enforce', '{"enforce": false}')
ON CONFLICT (key) DO NOTHING;

-- +goose Down
DELETE FROM system_settings WHERE key = 'billing.enforce';
DROP TABLE redemptions;
DROP TABLE redeem_codes;
DROP TABLE redeem_batches;
DROP TABLE reservations;
DROP TABLE ledger_entries;
DROP TABLE wallets;
