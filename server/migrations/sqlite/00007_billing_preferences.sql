-- +goose Up
-- SQLite edition of postgres/00007_billing_preferences.sql.
CREATE TABLE billing_preferences (
    user_id         TEXT PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    quota_overflow  TEXT NOT NULL DEFAULT 'block' CHECK (quota_overflow IN ('block', 'wallet')),
    updated_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now'))
) STRICT;

-- Rules no longer carry onExceed (ignored if present in old documents).
UPDATE plans SET rules = (SELECT json_group_array(json(json_remove(r.value, '$.onExceed'))) FROM json_each(plans.rules) r);
UPDATE subscriptions SET rules = (SELECT json_group_array(json(json_remove(r.value, '$.onExceed'))) FROM json_each(subscriptions.rules) r);

-- +goose Down
DROP TABLE billing_preferences;
