-- +goose Up
-- SQLite edition of postgres/00004_plugins.sql.

CREATE TABLE plugins (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT,
    plugin_key   TEXT NOT NULL UNIQUE,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    author       TEXT NOT NULL DEFAULT '',
    homepage     TEXT NOT NULL DEFAULT '',
    source       TEXT NOT NULL CHECK (source IN ('builtin', 'bundled', 'upload', 'editor')),
    status       TEXT NOT NULL DEFAULT 'enabled' CHECK (status IN ('enabled', 'disabled')),
    created_by   TEXT,
    version      INTEGER NOT NULL DEFAULT 1,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now'))
) STRICT;

CREATE TABLE plugin_versions (
    id            TEXT PRIMARY KEY,
    plugin_id     TEXT NOT NULL REFERENCES plugins (id) ON DELETE CASCADE,
    version       TEXT NOT NULL,
    manifest      TEXT NOT NULL CHECK (json_valid(manifest)),
    files         TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(files)),
    bundle        TEXT,
    content_hash  TEXT NOT NULL,
    risk          TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(risk)),
    approval      TEXT NOT NULL CHECK (approval IN ('pending', 'approved', 'rejected')),
    approved_by   TEXT,
    approved_at   TEXT,
    approval_note TEXT,
    published_by  TEXT,
    published_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    UNIQUE (plugin_id, version)
) STRICT;
CREATE INDEX plugin_versions_plugin_idx ON plugin_versions (plugin_id, published_at DESC);

CREATE TABLE plugin_drafts (
    plugin_id       TEXT PRIMARY KEY REFERENCES plugins (id) ON DELETE CASCADE,
    files           TEXT NOT NULL CHECK (json_valid(files)),
    base_version_id TEXT REFERENCES plugin_versions (id) ON DELETE SET NULL,
    updated_by      TEXT,
    updated_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    version         INTEGER NOT NULL DEFAULT 1
) STRICT;

CREATE TABLE plugin_storage (
    plugin_id  TEXT NOT NULL REFERENCES plugins (id) ON DELETE CASCADE,
    channel_id TEXT NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL CHECK (json_valid(value)),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    PRIMARY KEY (plugin_id, channel_id, key)
) STRICT;

CREATE TABLE capability_results (
    channel_id        TEXT NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    capability        TEXT NOT NULL,
    ok                INTEGER NOT NULL,
    unsupported       INTEGER NOT NULL DEFAULT 0,
    output            TEXT CHECK (output IS NULL OR json_valid(output)),
    error             TEXT,
    plugin_version_id TEXT,
    duration_ms       INTEGER NOT NULL DEFAULT 0,
    fetched_at        TEXT NOT NULL,
    PRIMARY KEY (channel_id, capability)
) STRICT;

ALTER TABLE channels ADD COLUMN plugin_version_id TEXT REFERENCES plugin_versions (id);
ALTER TABLE channels ADD COLUMN plugin_config TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(plugin_config));
CREATE INDEX channels_plugin_version_idx ON channels (plugin_version_id) WHERE plugin_version_id IS NOT NULL;

-- +goose Down
DROP INDEX channels_plugin_version_idx;
ALTER TABLE channels DROP COLUMN plugin_config;
ALTER TABLE channels DROP COLUMN plugin_version_id;
DROP TABLE capability_results;
DROP TABLE plugin_storage;
DROP TABLE plugin_drafts;
DROP TABLE plugin_versions;
DROP TABLE plugins;
