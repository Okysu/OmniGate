-- +goose Up
-- SQLite edition of postgres/00022_plan_groups.sql (phase17-api.md).
ALTER TABLE plans ADD COLUMN plan_group TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE plans DROP COLUMN plan_group;
