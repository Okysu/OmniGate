-- +goose Up
-- SQLite edition of postgres/00006_plans.sql. Quota usage (numeric(38,9) on
-- PostgreSQL) is exact decimal TEXT here; additions are done in Go inside the
-- settlement transaction. SQLite cannot alter CHECK constraints, so
-- redeem_batches is rebuilt (migrations run with foreign_keys off; the runner
-- checks foreign keys afterwards).

CREATE TABLE plans (
    id               TEXT PRIMARY KEY,
    workspace_id     TEXT,
    name             TEXT NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    list_price_nano  INTEGER CHECK (list_price_nano IS NULL OR list_price_nano >= 0),
    duration         TEXT NOT NULL,
    models           TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(models)),
    rules            TEXT NOT NULL CHECK (json_valid(rules)),
    stackable        INTEGER NOT NULL DEFAULT 0,
    status           TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    created_by       TEXT REFERENCES users (id),
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    version          INTEGER NOT NULL DEFAULT 1
) STRICT;
CREATE INDEX plans_status_idx ON plans (status, created_at DESC, id DESC);

CREATE TABLE subscriptions (
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
CREATE INDEX subscriptions_user_active_idx ON subscriptions (user_id, ends_at) WHERE status = 'active';
CREATE INDEX subscriptions_user_idx ON subscriptions (user_id, created_at DESC, id DESC);
CREATE INDEX subscriptions_plan_active_idx ON subscriptions (plan_id, ends_at) WHERE status = 'active';
CREATE INDEX subscriptions_created_idx ON subscriptions (created_at DESC, id DESC);

CREATE TABLE quota_usage (
    subscription_id  TEXT NOT NULL REFERENCES subscriptions (id) ON DELETE CASCADE,
    rule_id          TEXT NOT NULL,
    window_start     TEXT NOT NULL,
    used             TEXT NOT NULL DEFAULT '0',
    updated_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    PRIMARY KEY (subscription_id, rule_id, window_start)
) STRICT;

CREATE TABLE subscription_charges (
    request_id         TEXT PRIMARY KEY,
    subscription_id    TEXT NOT NULL REFERENCES subscriptions (id) ON DELETE CASCADE,
    model              TEXT NOT NULL,
    quota_charge_nano  INTEGER NOT NULL DEFAULT 0,
    created_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now'))
) STRICT;
CREATE INDEX subscription_charges_sub_idx ON subscription_charges (subscription_id, created_at DESC);

-- Redeem batches gain kind 'plan' ({planId, periods}).
CREATE TABLE redeem_batches_new (
    id                        TEXT PRIMARY KEY,
    workspace_id              TEXT,
    kind                      TEXT NOT NULL CONSTRAINT redeem_batches_kind_check CHECK (kind IN ('wallet_credit', 'plan')),
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
    plan_id                   TEXT REFERENCES plans (id),
    periods                   INTEGER,
    CHECK (valid_from IS NULL OR expires_at IS NULL OR expires_at > valid_from),
    CONSTRAINT redeem_batches_payload_check CHECK (
        (kind = 'wallet_credit' AND amount_nano IS NOT NULL AND amount_nano > 0 AND plan_id IS NULL AND periods IS NULL)
        OR (kind = 'plan' AND plan_id IS NOT NULL AND periods IS NOT NULL AND periods BETWEEN 1 AND 120
            AND amount_nano IS NULL))
) STRICT;
INSERT INTO redeem_batches_new (id, workspace_id, kind, amount_nano, payload, count, max_redemptions_per_code,
    per_user_limit, valid_from, expires_at, note, status, created_by, created_at, updated_at, version)
SELECT id, workspace_id, kind, amount_nano, payload, count, max_redemptions_per_code,
    per_user_limit, valid_from, expires_at, note, status, created_by, created_at, updated_at, version
FROM redeem_batches;
DROP TABLE redeem_batches;
ALTER TABLE redeem_batches_new RENAME TO redeem_batches;
CREATE INDEX redeem_batches_created_idx ON redeem_batches (created_at DESC, id DESC);
CREATE INDEX redeem_batches_plan_idx ON redeem_batches (plan_id) WHERE plan_id IS NOT NULL;

ALTER TABLE redemptions ADD COLUMN subscription_id TEXT REFERENCES subscriptions (id);

ALTER TABLE request_logs ADD COLUMN subscription_id TEXT;
ALTER TABLE request_logs ADD COLUMN quota_charge_nano INTEGER NOT NULL DEFAULT 0;
CREATE INDEX request_logs_subscription_idx ON request_logs (subscription_id, started_at DESC)
    WHERE subscription_id IS NOT NULL;

-- +goose Down
DROP INDEX request_logs_subscription_idx;
ALTER TABLE request_logs DROP COLUMN quota_charge_nano;
ALTER TABLE request_logs DROP COLUMN subscription_id;

ALTER TABLE redemptions DROP COLUMN subscription_id;

-- Plan batches cannot be represented in the previous schema.
DELETE FROM redemptions WHERE batch_id IN (SELECT id FROM redeem_batches WHERE kind = 'plan');
DELETE FROM redeem_codes WHERE batch_id IN (SELECT id FROM redeem_batches WHERE kind = 'plan');
DELETE FROM redeem_batches WHERE kind = 'plan';
CREATE TABLE redeem_batches_old (
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
INSERT INTO redeem_batches_old (id, workspace_id, kind, amount_nano, payload, count, max_redemptions_per_code,
    per_user_limit, valid_from, expires_at, note, status, created_by, created_at, updated_at, version)
SELECT id, workspace_id, kind, amount_nano, payload, count, max_redemptions_per_code,
    per_user_limit, valid_from, expires_at, note, status, created_by, created_at, updated_at, version
FROM redeem_batches;
DROP TABLE redeem_batches;
ALTER TABLE redeem_batches_old RENAME TO redeem_batches;
CREATE INDEX redeem_batches_created_idx ON redeem_batches (created_at DESC, id DESC);

DROP TABLE subscription_charges;
DROP TABLE quota_usage;
DROP TABLE subscriptions;
DROP TABLE plans;
