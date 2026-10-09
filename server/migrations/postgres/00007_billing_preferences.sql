-- +goose Up
-- Round 5: per-user billing preferences. quota_overflow decides what happens
-- when every covering subscription is out of quota: 'block' (HTTP 429) or
-- 'wallet' (fall back to pay-as-you-go from the wallet). API keys may override
-- it in their policy. Replaces the per-rule onExceed setting of Round 4.
CREATE TABLE billing_preferences (
    user_id         uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    quota_overflow  text NOT NULL DEFAULT 'block' CHECK (quota_overflow IN ('block', 'wallet')),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- Rules no longer carry onExceed (ignored if present in old documents).
UPDATE plans SET rules = (SELECT jsonb_agg(r - 'onExceed') FROM jsonb_array_elements(rules) r);
UPDATE subscriptions SET rules = (SELECT jsonb_agg(r - 'onExceed') FROM jsonb_array_elements(rules) r);

-- +goose Down
DROP TABLE billing_preferences;
