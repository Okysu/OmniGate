-- +goose Up
-- Plan groups (phase17-api.md): a subscription can only be upgraded to a plan of
-- the same group (e.g. "GPT" vs "国模"); '' is a group of its own.
ALTER TABLE plans ADD COLUMN plan_group text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE plans DROP COLUMN plan_group;
