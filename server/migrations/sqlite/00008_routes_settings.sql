-- +goose Up
-- SQLite edition of postgres/00008_routes_settings.sql. The ref_type CHECK of
-- ledger_entries changes, so the table is rebuilt (see 00006).

CREATE TABLE route_rules (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    enabled      INTEGER NOT NULL DEFAULT 1,
    position     INTEGER NOT NULL,
    spec         TEXT NOT NULL CHECK (json_valid(spec)),
    version      INTEGER NOT NULL DEFAULT 1,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    created_by   TEXT
) STRICT;
CREATE INDEX route_rules_position_idx ON route_rules (position);

ALTER TABLE request_logs ADD COLUMN served_model TEXT;

CREATE TABLE ledger_entries_new (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT,
    wallet_id           TEXT NOT NULL REFERENCES wallets (id),
    kind                TEXT NOT NULL CHECK (kind IN ('grant', 'charge', 'refund', 'adjust')),
    amount_nano         INTEGER NOT NULL,
    balance_after_nano  INTEGER NOT NULL,
    ref_type            TEXT NOT NULL CONSTRAINT ledger_entries_ref_type_check
                        CHECK (ref_type IN ('request', 'redeem', 'admin', 'signup')),
    ref_id              TEXT NOT NULL,
    note                TEXT,
    created_by          TEXT,
    created_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now'))
) STRICT;
INSERT INTO ledger_entries_new SELECT * FROM ledger_entries;
DROP TABLE ledger_entries;
ALTER TABLE ledger_entries_new RENAME TO ledger_entries;
CREATE INDEX ledger_entries_wallet_idx ON ledger_entries (wallet_id, created_at DESC, id DESC);
CREATE UNIQUE INDEX ledger_entries_charge_ref_uidx ON ledger_entries (ref_type, ref_id) WHERE kind = 'charge';
CREATE UNIQUE INDEX ledger_entries_signup_uidx ON ledger_entries (ref_id) WHERE ref_type = 'signup';

-- +goose Down
DROP INDEX ledger_entries_signup_uidx;
-- Keep sign-up grants (the wallet balance depends on them) as admin grants.
UPDATE ledger_entries SET ref_type = 'admin', note = COALESCE(note, 'signup credit') WHERE ref_type = 'signup';
CREATE TABLE ledger_entries_old (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT,
    wallet_id           TEXT NOT NULL REFERENCES wallets (id),
    kind                TEXT NOT NULL CHECK (kind IN ('grant', 'charge', 'refund', 'adjust')),
    amount_nano         INTEGER NOT NULL,
    balance_after_nano  INTEGER NOT NULL,
    ref_type            TEXT NOT NULL CONSTRAINT ledger_entries_ref_type_check
                        CHECK (ref_type IN ('request', 'redeem', 'admin')),
    ref_id              TEXT NOT NULL,
    note                TEXT,
    created_by          TEXT,
    created_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now'))
) STRICT;
INSERT INTO ledger_entries_old SELECT * FROM ledger_entries;
DROP TABLE ledger_entries;
ALTER TABLE ledger_entries_old RENAME TO ledger_entries;
CREATE INDEX ledger_entries_wallet_idx ON ledger_entries (wallet_id, created_at DESC, id DESC);
CREATE UNIQUE INDEX ledger_entries_charge_ref_uidx ON ledger_entries (ref_type, ref_id) WHERE kind = 'charge';
ALTER TABLE request_logs DROP COLUMN served_model;
DROP TABLE route_rules;
DELETE FROM system_settings WHERE key = 'system';
