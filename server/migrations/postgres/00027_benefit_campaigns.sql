-- +goose Up
ALTER TABLE plans ADD COLUMN stock bigint NOT NULL DEFAULT -1 CHECK (stock >= -1);
ALTER TABLE plans ADD COLUMN hidden boolean NOT NULL DEFAULT false;
CREATE TABLE benefit_campaigns (
 id uuid PRIMARY KEY, title TEXT NOT NULL, description TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('draft','active','archived')),
 reward_type TEXT NOT NULL CHECK(reward_type IN ('balance','subscription')),
 plan_id uuid REFERENCES plans(id), amount_nano bigint NOT NULL CHECK(amount_nano>=0),
 price_nano bigint NOT NULL CHECK(price_nano>=0), stock bigint NOT NULL DEFAULT -1 CHECK(stock>=-1),
 per_user_limit INTEGER NOT NULL DEFAULT 1 CHECK(per_user_limit>=1),
 starts_at timestamptz, ends_at timestamptz, conditions TEXT NOT NULL,
 version INTEGER NOT NULL DEFAULT 1, created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL
);
CREATE TABLE benefit_claims (
 id uuid PRIMARY KEY, campaign_id uuid NOT NULL REFERENCES benefit_campaigns(id),
 user_id uuid NOT NULL REFERENCES users(id), request_id uuid NOT NULL,
 subscription_id uuid REFERENCES subscriptions(id), ledger_id uuid REFERENCES ledger_entries(id),
 created_at timestamptz NOT NULL, UNIQUE(user_id,request_id)
);
CREATE INDEX benefit_claims_campaign_user ON benefit_claims(campaign_id,user_id);
-- +goose Down
DROP TABLE benefit_claims;
DROP TABLE benefit_campaigns;
ALTER TABLE plans DROP COLUMN hidden;
ALTER TABLE plans DROP COLUMN stock;
