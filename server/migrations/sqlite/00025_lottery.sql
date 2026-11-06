-- +goose Up
CREATE TABLE ledger_replace (
    id TEXT PRIMARY KEY, workspace_id TEXT, wallet_id TEXT NOT NULL REFERENCES wallets(id),
    kind TEXT NOT NULL CHECK (kind IN ('grant','charge','refund','adjust')), amount_nano INTEGER NOT NULL,
    balance_after_nano INTEGER NOT NULL, ref_type TEXT NOT NULL CHECK (ref_type IN ('request', 'redeem', 'admin', 'signup', 'purchase', 'referral', 'lottery')),
    ref_id TEXT NOT NULL, note TEXT, created_by TEXT, created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now'))
) STRICT;
INSERT INTO ledger_replace SELECT * FROM ledger_entries;
DROP TABLE ledger_entries;
ALTER TABLE ledger_replace RENAME TO ledger_entries;
CREATE INDEX ledger_entries_wallet_idx ON ledger_entries (wallet_id, created_at DESC, id DESC);
CREATE UNIQUE INDEX ledger_entries_charge_ref_uidx ON ledger_entries (ref_type, ref_id) WHERE kind = 'charge';
CREATE UNIQUE INDEX ledger_entries_signup_uidx ON ledger_entries (ref_id) WHERE ref_type = 'signup';
CREATE TABLE lottery_config (
    id INTEGER PRIMARY KEY CHECK (id = 1), threshold_nano INTEGER NOT NULL CHECK (threshold_nano >= 0),
    prizes TEXT NOT NULL, version INTEGER NOT NULL
) STRICT;
INSERT INTO lottery_config VALUES (1, 10000000000, '[{"amount":"0.5","percent":5},{"amount":"0.88","percent":10},{"amount":"1","percent":15},{"amount":"1.28","percent":25},{"amount":"1.68","percent":20},{"amount":"2","percent":15},{"amount":"2.5","percent":7},{"amount":"3","percent":3}]', 1);
CREATE TABLE lottery_days (
    user_id TEXT NOT NULL REFERENCES users(id), day TEXT NOT NULL,
    spent_nano INTEGER NOT NULL CHECK (spent_nano > 0), earned INTEGER NOT NULL,
    PRIMARY KEY (user_id, day)
) STRICT;
CREATE TABLE lottery_draws (
    user_id TEXT NOT NULL REFERENCES users(id), request_id TEXT NOT NULL,
    draw_count INTEGER NOT NULL CHECK (draw_count IN (1,5,10,20)), rewards TEXT NOT NULL,
    total_nano INTEGER NOT NULL CHECK (total_nano > 0), ledger_id TEXT NOT NULL UNIQUE REFERENCES ledger_entries(id),
    created_at TEXT NOT NULL, PRIMARY KEY(user_id, request_id)
) STRICT;
-- Wallet debits and subscription settlements survive request-log retention.
-- For subscriptions whose deduplication record has aged out, use the retained log.
INSERT INTO lottery_days (user_id, day, spent_nano, earned)
SELECT user_id, date(substr(started_at,1,19), '+8 hours'), SUM(spend), SUM(spend) > 10000000000 FROM (
    SELECT w.user_id, l.created_at AS started_at, -l.amount_nano AS spend
    FROM ledger_entries l JOIN wallets w ON w.id=l.wallet_id
    WHERE l.kind='charge' AND l.ref_type='request' AND l.amount_nano<0
    UNION ALL
    SELECT s.user_id, c.created_at, c.quota_charge_nano
    FROM subscription_charges c JOIN subscriptions s ON s.id=c.subscription_id WHERE c.quota_charge_nano>0
    UNION ALL
    SELECT r.user_id, r.started_at, r.quota_charge_nano FROM request_logs r
    WHERE r.user_id IS NOT NULL AND r.quota_charge_nano>0
    AND r.started_at < strftime('%Y-%m-%dT%H:%M:%f000000Z','now','-90 days')
    AND NOT EXISTS (SELECT 1 FROM subscription_charges c WHERE c.request_id=r.request_id)
) AS paid GROUP BY 1, 2;

-- +goose Down
DROP TABLE lottery_draws;
DROP TABLE lottery_days;
DROP TABLE lottery_config;
UPDATE ledger_entries SET ref_type='admin' WHERE ref_type='lottery';
CREATE TABLE ledger_replace (
    id TEXT PRIMARY KEY, workspace_id TEXT, wallet_id TEXT NOT NULL REFERENCES wallets(id),
    kind TEXT NOT NULL CHECK (kind IN ('grant','charge','refund','adjust')), amount_nano INTEGER NOT NULL,
    balance_after_nano INTEGER NOT NULL, ref_type TEXT NOT NULL CHECK (ref_type IN ('request', 'redeem', 'admin', 'signup', 'purchase', 'referral')),
    ref_id TEXT NOT NULL, note TEXT, created_by TEXT, created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now'))
) STRICT;
INSERT INTO ledger_replace SELECT * FROM ledger_entries;
DROP TABLE ledger_entries;
ALTER TABLE ledger_replace RENAME TO ledger_entries;
CREATE INDEX ledger_entries_wallet_idx ON ledger_entries (wallet_id, created_at DESC, id DESC);
CREATE UNIQUE INDEX ledger_entries_charge_ref_uidx ON ledger_entries (ref_type, ref_id) WHERE kind = 'charge';
CREATE UNIQUE INDEX ledger_entries_signup_uidx ON ledger_entries (ref_id) WHERE ref_type = 'signup';
