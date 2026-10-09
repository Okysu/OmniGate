-- +goose Up
-- Round 5 (continued, docs/contracts/phase5-api.md): model display information
-- for the model plaza, and the channel tier of each request log entry.

-- Display information for a logical model (§2). Rows are optional: models
-- without one are still callable and show up in the plaza with their name only.
CREATE TABLE model_info (
    model           text PRIMARY KEY,
    display_name    text NOT NULL DEFAULT '',
    description     text NOT NULL DEFAULT '',
    vendor          text NOT NULL DEFAULT '',
    tags            jsonb NOT NULL DEFAULT '[]',
    context_window  bigint,
    max_output      bigint,
    capabilities    jsonb NOT NULL DEFAULT '{}',
    hidden          boolean NOT NULL DEFAULT false,
    sort_order      integer NOT NULL DEFAULT 0,
    version         integer NOT NULL DEFAULT 1,
    created_at      timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL,
    updated_by      uuid
);

-- Tier of the channel that served the request relative to the requesting user
-- (own | shared | platform; §1). NULL when no channel was attempted and in rows
-- written before this migration.
ALTER TABLE request_logs ADD COLUMN channel_tier text
    CHECK (channel_tier IN ('own', 'shared', 'platform'));

-- +goose Down
ALTER TABLE request_logs DROP COLUMN channel_tier;
DROP TABLE model_info;
