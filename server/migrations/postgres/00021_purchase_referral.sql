-- +goose Up
-- Wallet plan purchases and referral rebates (phase15-api.md).

-- Subscriptions created by a wallet purchase.
ALTER TABLE subscriptions DROP CONSTRAINT subscriptions_source_check;
ALTER TABLE subscriptions ADD CONSTRAINT subscriptions_source_check
    CHECK (source IN ('admin', 'redeem', 'purchase'));

-- Purchase debits ('charge', ref purchase/<purchase id>) and referral rebates
-- ('grant', ref referral/<rebate id>).
ALTER TABLE ledger_entries DROP CONSTRAINT ledger_entries_ref_type_check;
ALTER TABLE ledger_entries ADD CONSTRAINT ledger_entries_ref_type_check
    CHECK (ref_type IN ('request', 'redeem', 'admin', 'signup', 'purchase', 'referral'));

-- One row per wallet purchase: new subscription, renewal or upgrade.
CREATE TABLE plan_purchases (
    id                    uuid PRIMARY KEY,
    user_id               uuid NOT NULL REFERENCES users (id),
    action                text NOT NULL CHECK (action IN ('new', 'renew', 'upgrade')),
    plan_id               uuid NOT NULL REFERENCES plans (id),
    plan_name             text NOT NULL,
    subscription_id       uuid REFERENCES subscriptions (id) ON DELETE SET NULL,
    from_plan_id          uuid REFERENCES plans (id),
    from_plan_name        text,
    price_nano            bigint NOT NULL CHECK (price_nano > 0),
    ledger_entry_id       uuid NOT NULL REFERENCES ledger_entries (id),
    created_at            timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX plan_purchases_user_idx ON plan_purchases (user_id, created_at DESC, id DESC);

-- Each user's permanent invite code (created on first use).
CREATE TABLE referral_codes (
    user_id     uuid PRIMARY KEY REFERENCES users (id),
    code        text NOT NULL UNIQUE,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- invitee → inviter, bound once when the invitee's account is created.
CREATE TABLE referrals (
    invitee_id  uuid PRIMARY KEY REFERENCES users (id),
    inviter_id  uuid NOT NULL REFERENCES users (id),
    created_at  timestamptz NOT NULL DEFAULT now(),
    CHECK (invitee_id <> inviter_id)
);
CREATE INDEX referrals_inviter_idx ON referrals (inviter_id, created_at DESC);

-- One rebate per qualifying wallet_credit redemption of an invitee.
CREATE TABLE referral_rebates (
    id               uuid PRIMARY KEY,
    inviter_id       uuid NOT NULL REFERENCES users (id),
    invitee_id       uuid NOT NULL REFERENCES users (id),
    redemption_id    uuid NOT NULL UNIQUE,
    recharge_nano    bigint NOT NULL CHECK (recharge_nano > 0),
    rate             text NOT NULL,
    rebate_nano      bigint NOT NULL CHECK (rebate_nano > 0),
    ledger_entry_id  uuid NOT NULL REFERENCES ledger_entries (id),
    created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX referral_rebates_inviter_idx ON referral_rebates (inviter_id, created_at DESC, id DESC);

-- +goose Down
DROP TABLE referral_rebates;
DROP TABLE referrals;
DROP TABLE referral_codes;
DROP TABLE plan_purchases;
-- Keep the balance-relevant entries as admin entries.
UPDATE ledger_entries SET ref_type = 'admin', note = COALESCE(note, ref_type) WHERE ref_type IN ('purchase', 'referral');
ALTER TABLE ledger_entries DROP CONSTRAINT ledger_entries_ref_type_check;
ALTER TABLE ledger_entries ADD CONSTRAINT ledger_entries_ref_type_check
    CHECK (ref_type IN ('request', 'redeem', 'admin', 'signup'));
UPDATE subscriptions SET source = 'admin' WHERE source = 'purchase';
ALTER TABLE subscriptions DROP CONSTRAINT subscriptions_source_check;
ALTER TABLE subscriptions ADD CONSTRAINT subscriptions_source_check CHECK (source IN ('admin', 'redeem'));
