-- +goose Up
-- The order in which the user wants their active subscriptions used
-- (0 = first); NULL = after the ordered ones, soonest to expire first.
ALTER TABLE subscriptions ADD COLUMN use_order integer;

-- +goose Down
ALTER TABLE subscriptions DROP COLUMN use_order;
