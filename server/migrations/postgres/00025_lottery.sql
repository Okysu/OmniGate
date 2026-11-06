-- +goose Up
ALTER TABLE ledger_entries DROP CONSTRAINT ledger_entries_ref_type_check;
ALTER TABLE ledger_entries ADD CONSTRAINT ledger_entries_ref_type_check CHECK (ref_type IN ('request', 'redeem', 'admin', 'signup', 'purchase', 'referral', 'lottery'));
CREATE TABLE lottery_config (
    id INTEGER PRIMARY KEY CHECK (id = 1), threshold_nano bigint NOT NULL CHECK (threshold_nano >= 0),
    prizes TEXT NOT NULL, version INTEGER NOT NULL
);
INSERT INTO lottery_config VALUES (1, 10000000000, '[{"amount":"0.5","percent":5},{"amount":"0.88","percent":10},{"amount":"1","percent":15},{"amount":"1.28","percent":25},{"amount":"1.68","percent":20},{"amount":"2","percent":15},{"amount":"2.5","percent":7},{"amount":"3","percent":3}]', 1);
CREATE TABLE lottery_days (
    user_id uuid NOT NULL REFERENCES users(id), day TEXT NOT NULL,
    spent_nano bigint NOT NULL CHECK (spent_nano > 0), earned boolean NOT NULL,
    PRIMARY KEY (user_id, day)
);
CREATE TABLE lottery_draws (
    user_id uuid NOT NULL REFERENCES users(id), request_id uuid NOT NULL,
    draw_count INTEGER NOT NULL CHECK (draw_count IN (1,5,10,20)), rewards TEXT NOT NULL,
    total_nano bigint NOT NULL CHECK (total_nano > 0), ledger_id uuid NOT NULL UNIQUE REFERENCES ledger_entries(id),
    created_at timestamptz NOT NULL, PRIMARY KEY(user_id, request_id)
);
-- Wallet debits and subscription settlements survive request-log retention.
-- For subscriptions whose deduplication record has aged out, use the retained log.
INSERT INTO lottery_days (user_id, day, spent_nano, earned)
SELECT user_id, to_char(started_at AT TIME ZONE 'Asia/Hong_Kong', 'YYYY-MM-DD'), SUM(spend), SUM(spend) > 10000000000 FROM (
    SELECT w.user_id, l.created_at AS started_at, -l.amount_nano AS spend
    FROM ledger_entries l JOIN wallets w ON w.id=l.wallet_id
    WHERE l.kind='charge' AND l.ref_type='request' AND l.amount_nano<0
    UNION ALL
    SELECT s.user_id, c.created_at, c.quota_charge_nano
    FROM subscription_charges c JOIN subscriptions s ON s.id=c.subscription_id WHERE c.quota_charge_nano>0
    UNION ALL
    SELECT r.user_id, r.started_at, r.quota_charge_nano FROM request_logs r
    WHERE r.user_id IS NOT NULL AND r.quota_charge_nano>0
    AND r.started_at < CURRENT_TIMESTAMP - INTERVAL '90 days'
    AND NOT EXISTS (SELECT 1 FROM subscription_charges c WHERE c.request_id=r.request_id)
) AS paid GROUP BY 1, 2;

-- +goose Down
DROP TABLE lottery_draws;
DROP TABLE lottery_days;
DROP TABLE lottery_config;
UPDATE ledger_entries SET ref_type='admin' WHERE ref_type='lottery';
ALTER TABLE ledger_entries DROP CONSTRAINT ledger_entries_ref_type_check;
ALTER TABLE ledger_entries ADD CONSTRAINT ledger_entries_ref_type_check CHECK (ref_type IN ('request', 'redeem', 'admin', 'signup', 'purchase', 'referral'));
