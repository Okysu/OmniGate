-- +goose Up
-- SQLite edition of postgres/00001_identity_audit.sql (ADR-0009). Type mapping:
-- uuid / timestamptz → TEXT (timestamps are UTC RFC 3339 with nine fractional
-- digits, so text order is time order), jsonb → TEXT checked with json_valid,
-- bytea → BLOB, boolean → INTEGER 0/1. Tables are STRICT.

CREATE TABLE system_settings (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL CHECK (json_valid(value)),
    version     INTEGER NOT NULL DEFAULT 1,
    updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_by  TEXT
) STRICT;

CREATE TABLE users (
    id             TEXT PRIMARY KEY,
    workspace_id   TEXT,
    display_name   TEXT NOT NULL,
    email          TEXT,
    avatar_url     TEXT,
    role           TEXT NOT NULL DEFAULT 'user'
                   CHECK (role IN ('system_admin', 'channel_admin', 'user', 'auditor')),
    status         TEXT NOT NULL DEFAULT 'active'
                   CHECK (status IN ('active', 'disabled')),
    version        INTEGER NOT NULL DEFAULT 1,
    created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    last_login_at  TEXT
) STRICT;
CREATE INDEX users_created_at_idx ON users (created_at DESC);
CREATE INDEX users_email_idx ON users (lower(email));

CREATE TABLE user_identities (
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    provider        TEXT NOT NULL,
    subject         TEXT NOT NULL,
    login           TEXT,
    email           TEXT,
    email_verified  INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    last_login_at   TEXT,
    UNIQUE (provider, subject)
) STRICT;
CREATE INDEX user_identities_user_idx ON user_identities (user_id);

CREATE TABLE sessions (
    id               TEXT PRIMARY KEY,
    user_id          TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash       BLOB NOT NULL UNIQUE,
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    last_seen_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    expires_at       TEXT NOT NULL,
    user_agent       TEXT NOT NULL DEFAULT '',
    ip_prefix        TEXT NOT NULL DEFAULT '',
    revoked_at       TEXT,
    revoked_reason   TEXT
) STRICT;
CREATE INDEX sessions_user_idx ON sessions (user_id) WHERE revoked_at IS NULL;

CREATE TABLE audit_logs (
    id             TEXT PRIMARY KEY,
    actor_id       TEXT,
    actor_name     TEXT,
    action         TEXT NOT NULL,
    resource_type  TEXT NOT NULL,
    resource_id    TEXT,
    ip_prefix      TEXT NOT NULL DEFAULT '',
    request_id     TEXT NOT NULL DEFAULT '',
    metadata       TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata)),
    created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now'))
) STRICT;
CREATE INDEX audit_logs_created_idx ON audit_logs (created_at DESC);
CREATE INDEX audit_logs_actor_idx ON audit_logs (actor_id, created_at DESC);
CREATE INDEX audit_logs_action_idx ON audit_logs (action, created_at DESC);

-- +goose Down
DROP TABLE audit_logs;
DROP TABLE sessions;
DROP TABLE user_identities;
DROP TABLE users;
DROP TABLE system_settings;
