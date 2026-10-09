-- +goose Up
-- Round 6 (docs/contracts/phase8-api.md §1–§3): user groups with a sell-price
-- multiplier and limits, channel sharing with groups, usage counters for the
-- request / spend limits and time-of-day price schedules.

-- §1: every user belongs to exactly one group; exactly one group is the
-- default (new users join it). Multiplier and spend limits are nano units
-- (1e-9) like every other amount; NULL limit = unlimited.
CREATE TABLE user_groups (
    id                     uuid PRIMARY KEY,
    name                   text NOT NULL,
    description            text NOT NULL DEFAULT '',
    price_multiplier_nano  bigint NOT NULL DEFAULT 1000000000
                           CHECK (price_multiplier_nano BETWEEN 0 AND 100000000000),
    rpm                    integer CHECK (rpm > 0),
    rpd                    integer CHECK (rpd > 0),
    daily_spend_nano       bigint CHECK (daily_spend_nano >= 0),
    monthly_spend_nano     bigint CHECK (monthly_spend_nano >= 0),
    timezone               text NOT NULL DEFAULT 'Asia/Shanghai',
    is_default             boolean NOT NULL DEFAULT false,
    version                integer NOT NULL DEFAULT 1,
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX user_groups_name_uidx ON user_groups (lower(name));
CREATE UNIQUE INDEX user_groups_default_uidx ON user_groups (is_default) WHERE is_default;

-- The initial default group (fixed id) holds every existing user.
INSERT INTO user_groups (id, name, is_default) VALUES ('01920000-0000-7000-8000-000000000001', '默认', true);

ALTER TABLE users ADD COLUMN group_id uuid REFERENCES user_groups (id);
UPDATE users SET group_id = '01920000-0000-7000-8000-000000000001';
ALTER TABLE users ALTER COLUMN group_id SET NOT NULL;
CREATE INDEX users_group_idx ON users (group_id);

-- Rows inserted without a group (other tools, fixtures) join the default group.
-- +goose StatementBegin
CREATE FUNCTION users_default_group() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.group_id IS NULL THEN
        SELECT id INTO NEW.group_id FROM user_groups WHERE is_default;
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER users_default_group BEFORE INSERT ON users
    FOR EACH ROW EXECUTE FUNCTION users_default_group();

-- §1.2: channels shared with whole groups (in addition to channel_shares).
CREATE TABLE channel_group_shares (
    channel_id  uuid NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    group_id    uuid NOT NULL REFERENCES user_groups (id) ON DELETE CASCADE,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, group_id)
);
CREATE INDEX channel_group_shares_group_idx ON channel_group_shares (group_id);

-- §2.2: request and spend counters per user / key and window, incremented in
-- the settlement transaction. window_start is the window's start instant in
-- the group's timezone (1970-01-01 UTC for 'total').
CREATE TABLE usage_counters (
    scope         text NOT NULL CHECK (scope IN ('user', 'key')),
    scope_id      uuid NOT NULL,
    window_kind   text NOT NULL CHECK (window_kind IN ('day', 'week', 'month', 'total')),
    window_start  timestamptz NOT NULL,
    requests      bigint NOT NULL DEFAULT 0,
    charge_nano   bigint NOT NULL DEFAULT 0,
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, scope_id, window_kind, window_start)
);
CREATE INDEX usage_counters_window_idx ON usage_counters (window_kind, window_start);

-- §3: time-of-day schedule of a price version (NULL = none).
ALTER TABLE prices ADD COLUMN schedule jsonb;
ALTER TABLE prices ADD COLUMN schedule_timezone text NOT NULL DEFAULT 'Asia/Shanghai';

-- §1.1: effective multiplier of the request (group × schedule), decimal text.
ALTER TABLE request_logs ADD COLUMN price_multiplier text;

-- +goose Down
ALTER TABLE request_logs DROP COLUMN price_multiplier;
ALTER TABLE prices DROP COLUMN schedule_timezone;
ALTER TABLE prices DROP COLUMN schedule;
DROP TABLE usage_counters;
DROP TABLE channel_group_shares;
DROP TRIGGER users_default_group ON users;
DROP FUNCTION users_default_group();
DROP INDEX users_group_idx;
ALTER TABLE users DROP COLUMN group_id;
DROP TABLE user_groups;
