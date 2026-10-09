-- +goose Up
-- SQLite edition of postgres/00017_price_tiers.sql (phase10-api.md §1).
ALTER TABLE prices ADD COLUMN tiers TEXT CHECK (tiers IS NULL OR json_valid(tiers));

ALTER TABLE request_logs ADD COLUMN price_tier INTEGER;

-- +goose Down
ALTER TABLE request_logs DROP COLUMN price_tier;
ALTER TABLE prices DROP COLUMN tiers;
