-- +goose Up
-- Context-length tiered pricing (phase10-api.md §1): the tiers of a price
-- version (NULL = none) and the tier a request was priced with.
ALTER TABLE prices ADD COLUMN tiers jsonb;

-- aboveInputTokens of the sell-price tier applied (NULL = base prices).
ALTER TABLE request_logs ADD COLUMN price_tier bigint;

-- +goose Down
ALTER TABLE request_logs DROP COLUMN price_tier;
ALTER TABLE prices DROP COLUMN tiers;
