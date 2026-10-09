-- +goose Up
-- Round 5: route rules, served model in request logs, sign-up credit ledger
-- references. System settings live in the existing system_settings table (one
-- document under key 'system', created by the app on first start).

-- Route rules (docs/contracts/phase4-api.md §2). spec holds match, targets,
-- strategy, protocolPreference, retry and fallbackModels as one JSON document;
-- it is only ever read whole (never queried by content).
CREATE TABLE route_rules (
    id           uuid PRIMARY KEY,
    name         text NOT NULL,
    description  text NOT NULL DEFAULT '',
    enabled      boolean NOT NULL DEFAULT true,
    position     integer NOT NULL,
    spec         jsonb NOT NULL,
    version      integer NOT NULL DEFAULT 1,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    created_by   uuid
);
CREATE INDEX route_rules_position_idx ON route_rules (position);

-- The logical model that actually served the request (differs from model after
-- a fallback). NULL in rows written before this migration (= model).
ALTER TABLE request_logs ADD COLUMN served_model text;

-- Sign-up credit: a 'grant' ledger entry referencing the new user, at most once.
ALTER TABLE ledger_entries DROP CONSTRAINT ledger_entries_ref_type_check;
ALTER TABLE ledger_entries ADD CONSTRAINT ledger_entries_ref_type_check
    CHECK (ref_type IN ('request', 'redeem', 'admin', 'signup'));
CREATE UNIQUE INDEX ledger_entries_signup_uidx ON ledger_entries (ref_id) WHERE ref_type = 'signup';

-- +goose Down
DROP INDEX ledger_entries_signup_uidx;
-- Keep sign-up grants (the wallet balance depends on them) as admin grants.
UPDATE ledger_entries SET ref_type = 'admin', note = COALESCE(note, 'signup credit') WHERE ref_type = 'signup';
ALTER TABLE ledger_entries DROP CONSTRAINT ledger_entries_ref_type_check;
ALTER TABLE ledger_entries ADD CONSTRAINT ledger_entries_ref_type_check
    CHECK (ref_type IN ('request', 'redeem', 'admin'));
ALTER TABLE request_logs DROP COLUMN served_model;
DROP TABLE route_rules;
DELETE FROM system_settings WHERE key = 'system';
