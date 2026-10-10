-- +goose Up
-- Session affinity (phase12-api.md §4): the outcome for the request
-- (hit | new | miss | rebound | failover | broken | strict_failed | off) and
-- the applying rule's name; NULL when no rule applied. The client's session
-- value is never stored.
ALTER TABLE request_logs ADD COLUMN affinity text;
ALTER TABLE request_logs ADD COLUMN affinity_rule text;

-- +goose Down
ALTER TABLE request_logs DROP COLUMN affinity_rule;
ALTER TABLE request_logs DROP COLUMN affinity;
