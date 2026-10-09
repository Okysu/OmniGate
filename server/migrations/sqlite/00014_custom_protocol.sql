-- +goose Up
-- SQLite edition of postgres/00014_custom_protocol.sql: channel type "custom"
-- (docs/contracts/phase9-api.md §2). SQLite cannot alter a CHECK constraint,
-- so the table is rebuilt (see 00006 / 00008); referencing tables keep
-- pointing at "channels".
CREATE TABLE channels_new (
    id                   TEXT PRIMARY KEY,
    workspace_id         TEXT,
    owner_id             TEXT NOT NULL REFERENCES users (id),
    name                 TEXT NOT NULL,
    type                 TEXT NOT NULL CHECK (type IN ('openai', 'anthropic', 'custom')),
    base_url             TEXT NOT NULL,
    config               TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(config)),
    scope                TEXT NOT NULL DEFAULT 'private' CHECK (scope IN ('private', 'shared', 'global')),
    status               TEXT NOT NULL DEFAULT 'enabled' CHECK (status IN ('enabled', 'disabled')),
    priority             INTEGER NOT NULL DEFAULT 0,
    weight               INTEGER NOT NULL DEFAULT 1 CHECK (weight BETWEEN 1 AND 1000),
    version              INTEGER NOT NULL DEFAULT 1,
    created_at           TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at           TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    plugin_version_id    TEXT REFERENCES plugin_versions (id),
    plugin_config        TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(plugin_config)),
    alert_balance_below  TEXT
) STRICT;
INSERT INTO channels_new (id, workspace_id, owner_id, name, type, base_url, config, scope, status, priority, weight, version, created_at, updated_at, plugin_version_id, plugin_config, alert_balance_below)
    SELECT id, workspace_id, owner_id, name, type, base_url, config, scope, status, priority, weight, version, created_at, updated_at, plugin_version_id, plugin_config, alert_balance_below FROM channels;
DROP TABLE channels;
ALTER TABLE channels_new RENAME TO channels;
CREATE INDEX channels_owner_idx ON channels (owner_id);
CREATE INDEX channels_scope_idx ON channels (scope) WHERE scope <> 'private';
CREATE INDEX channels_plugin_version_idx ON channels (plugin_version_id) WHERE plugin_version_id IS NOT NULL;

-- +goose Down
CREATE TABLE channels_new (
    id                   TEXT PRIMARY KEY,
    workspace_id         TEXT,
    owner_id             TEXT NOT NULL REFERENCES users (id),
    name                 TEXT NOT NULL,
    type                 TEXT NOT NULL CHECK (type IN ('openai', 'anthropic')),
    base_url             TEXT NOT NULL,
    config               TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(config)),
    scope                TEXT NOT NULL DEFAULT 'private' CHECK (scope IN ('private', 'shared', 'global')),
    status               TEXT NOT NULL DEFAULT 'enabled' CHECK (status IN ('enabled', 'disabled')),
    priority             INTEGER NOT NULL DEFAULT 0,
    weight               INTEGER NOT NULL DEFAULT 1 CHECK (weight BETWEEN 1 AND 1000),
    version              INTEGER NOT NULL DEFAULT 1,
    created_at           TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at           TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    plugin_version_id    TEXT REFERENCES plugin_versions (id),
    plugin_config        TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(plugin_config)),
    alert_balance_below  TEXT
) STRICT;
UPDATE channels SET type = 'openai' WHERE type = 'custom';
INSERT INTO channels_new (id, workspace_id, owner_id, name, type, base_url, config, scope, status, priority, weight, version, created_at, updated_at, plugin_version_id, plugin_config, alert_balance_below)
    SELECT id, workspace_id, owner_id, name, type, base_url, config, scope, status, priority, weight, version, created_at, updated_at, plugin_version_id, plugin_config, alert_balance_below FROM channels;
DROP TABLE channels;
ALTER TABLE channels_new RENAME TO channels;
CREATE INDEX channels_owner_idx ON channels (owner_id);
CREATE INDEX channels_scope_idx ON channels (scope) WHERE scope <> 'private';
CREATE INDEX channels_plugin_version_idx ON channels (plugin_version_id) WHERE plugin_version_id IS NOT NULL;
