-- +goose Up
-- SQLite edition of postgres/00010_notifications.sql.

CREATE TABLE notification_preferences (
    user_id               TEXT PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    email_enabled         INTEGER NOT NULL DEFAULT 1,
    email_address         TEXT,
    webhook_enabled       INTEGER NOT NULL DEFAULT 0,
    webhook_url           TEXT,
    webhook_format        TEXT NOT NULL DEFAULT 'json'
                          CHECK (webhook_format IN ('json', 'feishu', 'dingtalk', 'wecom', 'slack')),
    events                TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(events)),
    wallet_threshold_nano INTEGER NOT NULL DEFAULT 1000000000,
    digest                TEXT NOT NULL DEFAULT 'off' CHECK (digest IN ('off', 'daily')),
    timezone              TEXT NOT NULL DEFAULT 'Asia/Shanghai',
    version               INTEGER NOT NULL DEFAULT 1,
    updated_at            TEXT NOT NULL
) STRICT;

CREATE TABLE notification_webhook_secrets (
    user_id     TEXT PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    ciphertext  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
) STRICT;

CREATE TABLE notification_email_codes (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    address     TEXT NOT NULL,
    code_hash   TEXT NOT NULL,
    attempts    INTEGER NOT NULL DEFAULT 0,
    expires_at  TEXT NOT NULL,
    used_at     TEXT,
    created_at  TEXT NOT NULL
) STRICT;
CREATE INDEX notification_email_codes_user_idx ON notification_email_codes (user_id, created_at);

CREATE TABLE notification_events (
    id          TEXT PRIMARY KEY,
    type        TEXT NOT NULL,
    dedupe_key  TEXT NOT NULL UNIQUE,
    subject     TEXT NOT NULL DEFAULT '',
    severity    TEXT NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
    title       TEXT NOT NULL,
    body        TEXT NOT NULL,
    link        TEXT,
    data        TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(data)),
    created_at  TEXT NOT NULL
) STRICT;
CREATE INDEX notification_events_subject_idx ON notification_events (type, subject, created_at);
CREATE INDEX notification_events_created_idx ON notification_events (created_at);

CREATE TABLE notifications (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    event_id    TEXT NOT NULL REFERENCES notification_events (id) ON DELETE CASCADE,
    type        TEXT NOT NULL,
    severity    TEXT NOT NULL,
    read_at     TEXT,
    created_at  TEXT NOT NULL,
    UNIQUE (user_id, event_id)
) STRICT;
CREATE INDEX notifications_user_idx ON notifications (user_id, created_at DESC);
CREATE INDEX notifications_unread_idx ON notifications (user_id) WHERE read_at IS NULL;

CREATE TABLE notification_deliveries (
    id               TEXT PRIMARY KEY,
    user_id          TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    event_id         TEXT REFERENCES notification_events (id) ON DELETE CASCADE,
    channel          TEXT NOT NULL CHECK (channel IN ('email', 'webhook')),
    kind             TEXT NOT NULL DEFAULT 'event' CHECK (kind IN ('event', 'digest', 'summary')),
    type             TEXT NOT NULL,
    status           TEXT NOT NULL CHECK (status IN ('pending', 'sending', 'sent', 'failed', 'skipped', 'held', 'merged')),
    attempts         INTEGER NOT NULL DEFAULT 0,
    next_attempt_at  TEXT NOT NULL,
    last_error       TEXT,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL,
    sent_at          TEXT
) STRICT;
CREATE INDEX notification_deliveries_due_idx ON notification_deliveries (status, next_attempt_at);
CREATE INDEX notification_deliveries_user_idx ON notification_deliveries (user_id, channel, status);
CREATE UNIQUE INDEX notification_deliveries_summary_idx ON notification_deliveries (user_id)
    WHERE kind = 'summary' AND status IN ('pending', 'sending');

CREATE TABLE notification_state (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(value)),
    updated_at  TEXT NOT NULL
) STRICT;

ALTER TABLE channels ADD COLUMN alert_balance_below TEXT;

-- +goose Down
ALTER TABLE channels DROP COLUMN alert_balance_below;
DROP TABLE notification_state;
DROP TABLE notification_deliveries;
DROP TABLE notifications;
DROP TABLE notification_events;
DROP TABLE notification_email_codes;
DROP TABLE notification_webhook_secrets;
DROP TABLE notification_preferences;
