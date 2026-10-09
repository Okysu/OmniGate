-- +goose Up
-- Round 6 (docs/contracts/phase6-api.md): notifications (email / webhook /
-- in-app), notification preferences, and per-channel alert settings.

-- Per-user preferences (§3). A missing row means "all defaults" (version 0).
-- email_address is a verified custom address (NULL = the account email).
CREATE TABLE notification_preferences (
    user_id               uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    email_enabled         boolean NOT NULL DEFAULT true,
    email_address         text,
    webhook_enabled       boolean NOT NULL DEFAULT false,
    webhook_url           text,
    webhook_format        text NOT NULL DEFAULT 'json'
                          CHECK (webhook_format IN ('json', 'feishu', 'dingtalk', 'wecom', 'slack')),
    events                jsonb NOT NULL DEFAULT '{}',
    wallet_threshold_nano bigint NOT NULL DEFAULT 1000000000,
    digest                text NOT NULL DEFAULT 'off' CHECK (digest IN ('off', 'daily')),
    timezone              text NOT NULL DEFAULT 'Asia/Shanghai',
    version               integer NOT NULL DEFAULT 1,
    updated_at            timestamptz NOT NULL
);

-- Webhook HMAC secrets (write-only, sealed with the master key, ADR-0008).
-- Kept apart from the preferences so setting a secret does not bump their version.
CREATE TABLE notification_webhook_secrets (
    user_id     uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    ciphertext  text NOT NULL,
    updated_at  timestamptz NOT NULL
);

-- Email verification codes: only a hash of the 6-digit code is stored.
CREATE TABLE notification_email_codes (
    id          uuid PRIMARY KEY,
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    address     text NOT NULL,
    code_hash   text NOT NULL,
    attempts    integer NOT NULL DEFAULT 0,
    expires_at  timestamptz NOT NULL,
    used_at     timestamptz,
    created_at  timestamptz NOT NULL
);
CREATE INDEX notification_email_codes_user_idx ON notification_email_codes (user_id, created_at);

-- One row per event; dedupe_key makes every emission idempotent (§2, §6).
-- subject groups events for throttling (e.g. the channel id).
CREATE TABLE notification_events (
    id          uuid PRIMARY KEY,
    type        text NOT NULL,
    dedupe_key  text NOT NULL UNIQUE,
    subject     text NOT NULL DEFAULT '',
    severity    text NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
    title       text NOT NULL,
    body        text NOT NULL,
    link        text,
    data        jsonb NOT NULL DEFAULT '{}',
    created_at  timestamptz NOT NULL
);
CREATE INDEX notification_events_subject_idx ON notification_events (type, subject, created_at);
CREATE INDEX notification_events_created_idx ON notification_events (created_at);

-- In-app notifications (§4), one per recipient.
CREATE TABLE notifications (
    id          uuid PRIMARY KEY,
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    event_id    uuid NOT NULL REFERENCES notification_events (id) ON DELETE CASCADE,
    type        text NOT NULL,
    severity    text NOT NULL,
    read_at     timestamptz,
    created_at  timestamptz NOT NULL,
    UNIQUE (user_id, event_id)
);
CREATE INDEX notifications_user_idx ON notifications (user_id, created_at DESC);
CREATE INDEX notifications_unread_idx ON notifications (user_id) WHERE read_at IS NULL;

-- Email / webhook outbox (§6). kind: event (one notification), digest (daily
-- digest item), summary (rate-limit overflow summary). status: pending →
-- sending (claimed, lease in next_attempt_at) → sent | failed | skipped;
-- held = over the hourly email limit, waiting for the summary; merged = sent
-- as part of a summary.
CREATE TABLE notification_deliveries (
    id               uuid PRIMARY KEY,
    user_id          uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    event_id         uuid REFERENCES notification_events (id) ON DELETE CASCADE,
    channel          text NOT NULL CHECK (channel IN ('email', 'webhook')),
    kind             text NOT NULL DEFAULT 'event' CHECK (kind IN ('event', 'digest', 'summary')),
    type             text NOT NULL,
    status           text NOT NULL CHECK (status IN ('pending', 'sending', 'sent', 'failed', 'skipped', 'held', 'merged')),
    attempts         integer NOT NULL DEFAULT 0,
    next_attempt_at  timestamptz NOT NULL,
    last_error       text,
    created_at       timestamptz NOT NULL,
    updated_at       timestamptz NOT NULL,
    sent_at          timestamptz
);
CREATE INDEX notification_deliveries_due_idx ON notification_deliveries (status, next_attempt_at);
CREATE INDEX notification_deliveries_user_idx ON notification_deliveries (user_id, channel, status);
CREATE UNIQUE INDEX notification_deliveries_summary_idx ON notification_deliveries (user_id)
    WHERE kind = 'summary' AND status IN ('pending', 'sending');

-- Small key/value state of the notification scanners (re-arm marks, known
-- model sets).
CREATE TABLE notification_state (
    key         text PRIMARY KEY,
    value       jsonb NOT NULL DEFAULT '{}',
    updated_at  timestamptz NOT NULL
);

-- Upstream balance alert threshold (§5), decimal text in the plugin's currency.
ALTER TABLE channels ADD COLUMN alert_balance_below text;

-- +goose Down
ALTER TABLE channels DROP COLUMN alert_balance_below;
DROP TABLE notification_state;
DROP TABLE notification_deliveries;
DROP TABLE notifications;
DROP TABLE notification_events;
DROP TABLE notification_email_codes;
DROP TABLE notification_webhook_secrets;
DROP TABLE notification_preferences;
