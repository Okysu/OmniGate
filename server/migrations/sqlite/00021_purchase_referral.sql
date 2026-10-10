-- +goose Up
-- SQLite edition of postgres/00021_purchase_referral.sql (phase15-api.md). The
-- CHECKs of subscriptions.source and ledger_entries.ref_type change, so both
-- tables are rebuilt (migrations run with foreign_keys off, see 00006).

CREATE TABLE subscriptions_new (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT,
    user_id       TEXT NOT NULL REFERENCES users (id),
    plan_id       TEXT NOT NULL REFERENCES plans (id),
    plan_name     TEXT NOT NULL,
    models        TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(models)),
    rules         TEXT NOT NULL CHECK (json_valid(rules)),
    starts_at     TEXT NOT NULL,
    ends_at       TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'cancelled')),
    source        TEXT NOT NULL CHECK (source IN ('admin', 'redeem', 'purchase')),
    source_ref    TEXT,
    cancelled_at  TEXT,
    cancelled_by  TEXT REFERENCES users (id),
    cancel_note   TEXT,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    version       INTEGER NOT NULL DEFAULT 1,
    CHECK (ends_at > starts_at),
    CHECK (status <> 'cancelled' OR cancelled_at IS NOT NULL)
) STRICT;
INSERT INTO subscriptions_new SELECT * FROM subscriptions;
DROP TABLE subscriptions;
ALTER TABLE subscriptions_new RENAME TO subscriptions;
CREATE INDEX subscriptions_user_active_idx ON subscriptions (user_id, ends_at) WHERE status = 'active';
CREATE INDEX subscriptions_user_idx ON subscriptions (user_id, created_at DESC, id DESC);
CREATE INDEX subscriptions_plan_active_idx ON subscriptions (plan_id, ends_at) WHERE status = 'active';
CREATE INDEX subscriptions_created_idx ON subscriptions (created_at DESC, id DESC);

CREATE TABLE ledger_entries_new (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT,
    wallet_id           TEXT NOT NULL REFERENCES wallets (id),
    kind                TEXT NOT NULL CHECK (kind IN ('grant', 'charge', 'refund', 'adjust')),
    amount_nano         INTEGER NOT NULL,
    balance_after_nano  INTEGER NOT NULL,
    ref_type            TEXT NOT NULL CONSTRAINT ledger_entries_ref_type_check
                        CHECK (ref_type IN ('request', 'redeem', 'admin', 'signup', 'purchase', 'referral')),
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

CREATE TABLE plan_purchases (
    id                    TEXT PRIMARY KEY,
    user_id               TEXT NOT NULL REFERENCES users (id),
    action                TEXT NOT NULL CHECK (action IN ('new', 'renew', 'upgrade')),
    plan_id               TEXT NOT NULL REFERENCES plans (id),
    plan_name             TEXT NOT NULL,
    subscription_id       TEXT REFERENCES subscriptions (id) ON DELETE SET NULL,
    from_plan_id          TEXT REFERENCES plans (id),
    from_plan_name        TEXT,
    price_nano            INTEGER NOT NULL CHECK (price_nano > 0),
    ledger_entry_id       TEXT NOT NULL REFERENCES ledger_entries (id),
    created_at            TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now'))
) STRICT;
CREATE INDEX plan_purchases_user_idx ON plan_purchases (user_id, created_at DESC, id DESC);

CREATE TABLE referral_codes (
    user_id     TEXT PRIMARY KEY REFERENCES users (id),
    code        TEXT NOT NULL UNIQUE,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now'))
) STRICT;

CREATE TABLE referrals (
    invitee_id  TEXT PRIMARY KEY REFERENCES users (id),
    inviter_id  TEXT NOT NULL REFERENCES users (id),
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    CHECK (invitee_id <> inviter_id)
) STRICT;
CREATE INDEX referrals_inviter_idx ON referrals (inviter_id, created_at DESC);

CREATE TABLE referral_rebates (
    id               TEXT PRIMARY KEY,
    inviter_id       TEXT NOT NULL REFERENCES users (id),
    invitee_id       TEXT NOT NULL REFERENCES users (id),
    redemption_id    TEXT NOT NULL UNIQUE,
    recharge_nano    INTEGER NOT NULL CHECK (recharge_nano > 0),
    rate             TEXT NOT NULL,
    rebate_nano      INTEGER NOT NULL CHECK (rebate_nano > 0),
    ledger_entry_id  TEXT NOT NULL REFERENCES ledger_entries (id),
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now'))
) STRICT;
CREATE INDEX referral_rebates_inviter_idx ON referral_rebates (inviter_id, created_at DESC, id DESC);

-- +goose Down
DROP TABLE referral_rebates;
DROP TABLE referrals;
DROP TABLE referral_codes;
DROP TABLE plan_purchases;

UPDATE ledger_entries SET ref_type = 'admin', note = COALESCE(note, ref_type) WHERE ref_type IN ('purchase', 'referral');
CREATE TABLE ledger_entries_old (
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
INSERT INTO ledger_entries_old SELECT * FROM ledger_entries;
DROP TABLE ledger_entries;
ALTER TABLE ledger_entries_old RENAME TO ledger_entries;
CREATE INDEX ledger_entries_wallet_idx ON ledger_entries (wallet_id, created_at DESC, id DESC);
CREATE UNIQUE INDEX ledger_entries_charge_ref_uidx ON ledger_entries (ref_type, ref_id) WHERE kind = 'charge';
CREATE UNIQUE INDEX ledger_entries_signup_uidx ON ledger_entries (ref_id) WHERE ref_type = 'signup';

UPDATE subscriptions SET source = 'admin' WHERE source = 'purchase';
CREATE TABLE subscriptions_old (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT,
    user_id       TEXT NOT NULL REFERENCES users (id),
    plan_id       TEXT NOT NULL REFERENCES plans (id),
    plan_name     TEXT NOT NULL,
    models        TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(models)),
    rules         TEXT NOT NULL CHECK (json_valid(rules)),
    starts_at     TEXT NOT NULL,
    ends_at       TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'cancelled')),
    source        TEXT NOT NULL CHECK (source IN ('admin', 'redeem')),
    source_ref    TEXT,
    cancelled_at  TEXT,
    cancelled_by  TEXT REFERENCES users (id),
    cancel_note   TEXT,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    version       INTEGER NOT NULL DEFAULT 1,
    CHECK (ends_at > starts_at),
    CHECK (status <> 'cancelled' OR cancelled_at IS NOT NULL)
) STRICT;
INSERT INTO subscriptions_old SELECT * FROM subscriptions;
DROP TABLE subscriptions;
ALTER TABLE subscriptions_old RENAME TO subscriptions;
CREATE INDEX subscriptions_user_active_idx ON subscriptions (user_id, ends_at) WHERE status = 'active';
CREATE INDEX subscriptions_user_idx ON subscriptions (user_id, created_at DESC, id DESC);
CREATE INDEX subscriptions_plan_active_idx ON subscriptions (plan_id, ends_at) WHERE status = 'active';
CREATE INDEX subscriptions_created_idx ON subscriptions (created_at DESC, id DESC);
