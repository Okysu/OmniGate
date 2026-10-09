-- +goose Up
-- SQLite edition of postgres/00012_groups_limits.sql. users.group_id stays
-- nullable (SQLite cannot add a NOT NULL column with a foreign key, and its
-- triggers cannot rewrite NEW): an AFTER INSERT trigger puts users inserted
-- without a group into the default group, and the application always sets it.

CREATE TABLE user_groups (
    id                     TEXT PRIMARY KEY,
    name                   TEXT NOT NULL,
    description            TEXT NOT NULL DEFAULT '',
    price_multiplier_nano  INTEGER NOT NULL DEFAULT 1000000000
                           CHECK (price_multiplier_nano BETWEEN 0 AND 100000000000),
    rpm                    INTEGER CHECK (rpm > 0),
    rpd                    INTEGER CHECK (rpd > 0),
    daily_spend_nano       INTEGER CHECK (daily_spend_nano >= 0),
    monthly_spend_nano     INTEGER CHECK (monthly_spend_nano >= 0),
    timezone               TEXT NOT NULL DEFAULT 'Asia/Shanghai',
    is_default             INTEGER NOT NULL DEFAULT 0,
    version                INTEGER NOT NULL DEFAULT 1,
    created_at             TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at             TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now'))
) STRICT;
CREATE UNIQUE INDEX user_groups_name_uidx ON user_groups (lower(name));
CREATE UNIQUE INDEX user_groups_default_uidx ON user_groups (is_default) WHERE is_default;

INSERT INTO user_groups (id, name, is_default) VALUES ('01920000-0000-7000-8000-000000000001', '默认', 1);

ALTER TABLE users ADD COLUMN group_id TEXT REFERENCES user_groups (id);
UPDATE users SET group_id = '01920000-0000-7000-8000-000000000001';
CREATE INDEX users_group_idx ON users (group_id);

-- +goose StatementBegin
CREATE TRIGGER users_default_group AFTER INSERT ON users WHEN NEW.group_id IS NULL
BEGIN
    UPDATE users SET group_id = (SELECT id FROM user_groups WHERE is_default) WHERE id = NEW.id;
END;
-- +goose StatementEnd

CREATE TABLE channel_group_shares (
    channel_id  TEXT NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    group_id    TEXT NOT NULL REFERENCES user_groups (id) ON DELETE CASCADE,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    PRIMARY KEY (channel_id, group_id)
) STRICT;
CREATE INDEX channel_group_shares_group_idx ON channel_group_shares (group_id);

CREATE TABLE usage_counters (
    scope         TEXT NOT NULL CHECK (scope IN ('user', 'key')),
    scope_id      TEXT NOT NULL,
    window_kind   TEXT NOT NULL CHECK (window_kind IN ('day', 'week', 'month', 'total')),
    window_start  TEXT NOT NULL,
    requests      INTEGER NOT NULL DEFAULT 0,
    charge_nano   INTEGER NOT NULL DEFAULT 0,
    updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    PRIMARY KEY (scope, scope_id, window_kind, window_start)
) STRICT;
CREATE INDEX usage_counters_window_idx ON usage_counters (window_kind, window_start);

ALTER TABLE prices ADD COLUMN schedule TEXT CHECK (schedule IS NULL OR json_valid(schedule));
ALTER TABLE prices ADD COLUMN schedule_timezone TEXT NOT NULL DEFAULT 'Asia/Shanghai';

ALTER TABLE request_logs ADD COLUMN price_multiplier TEXT;

-- +goose Down
ALTER TABLE request_logs DROP COLUMN price_multiplier;
ALTER TABLE prices DROP COLUMN schedule_timezone;
ALTER TABLE prices DROP COLUMN schedule;
DROP TABLE usage_counters;
DROP TABLE channel_group_shares;
DROP TRIGGER users_default_group;
DROP INDEX users_group_idx;
-- group_id has a foreign key, so it cannot be dropped: rebuild users (see 00006).
CREATE TABLE users_old (
    id               TEXT PRIMARY KEY,
    workspace_id     TEXT,
    display_name     TEXT NOT NULL,
    email            TEXT,
    avatar_url       TEXT,
    role             TEXT NOT NULL DEFAULT 'user'
                     CHECK (role IN ('system_admin', 'channel_admin', 'user', 'auditor')),
    status           TEXT NOT NULL DEFAULT 'active'
                     CHECK (status IN ('active', 'disabled')),
    version          INTEGER NOT NULL DEFAULT 1,
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    last_login_at    TEXT,
    disabled_reason  TEXT,
    disabled_until   TEXT
) STRICT;
INSERT INTO users_old (id, workspace_id, display_name, email, avatar_url, role, status, version, created_at, updated_at,
    last_login_at, disabled_reason, disabled_until)
SELECT id, workspace_id, display_name, email, avatar_url, role, status, version, created_at, updated_at,
    last_login_at, disabled_reason, disabled_until
FROM users;
DROP TABLE users;
ALTER TABLE users_old RENAME TO users;
CREATE INDEX users_created_at_idx ON users (created_at DESC);
CREATE INDEX users_email_idx ON users (lower(email));
CREATE INDEX users_disabled_until_idx ON users (disabled_until)
    WHERE status = 'disabled' AND disabled_until IS NOT NULL;
DROP TABLE user_groups;
