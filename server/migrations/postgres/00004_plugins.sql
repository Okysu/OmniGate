-- +goose Up
-- Round 3 (Phase 2): channel plugins (docs/contracts/phase2-api.md §3).

CREATE TABLE plugins (
    id           uuid PRIMARY KEY,
    workspace_id uuid,
    plugin_key   text NOT NULL UNIQUE,
    name         text NOT NULL,
    description  text NOT NULL DEFAULT '',
    author       text NOT NULL DEFAULT '',
    homepage     text NOT NULL DEFAULT '',
    source       text NOT NULL CHECK (source IN ('builtin', 'bundled', 'upload', 'editor')),
    status       text NOT NULL DEFAULT 'enabled' CHECK (status IN ('enabled', 'disabled')),
    created_by   uuid,
    version      integer NOT NULL DEFAULT 1,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- Published versions are immutable.
CREATE TABLE plugin_versions (
    id            uuid PRIMARY KEY,
    plugin_id     uuid NOT NULL REFERENCES plugins (id) ON DELETE CASCADE,
    version       text NOT NULL,
    manifest      jsonb NOT NULL,
    files         jsonb NOT NULL DEFAULT '{}'::jsonb,
    bundle        text,
    content_hash  text NOT NULL,
    risk          jsonb NOT NULL DEFAULT '[]'::jsonb,
    approval      text NOT NULL CHECK (approval IN ('pending', 'approved', 'rejected')),
    approved_by   uuid,
    approved_at   timestamptz,
    approval_note text,
    published_by  uuid,
    published_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (plugin_id, version)
);
CREATE INDEX plugin_versions_plugin_idx ON plugin_versions (plugin_id, published_at DESC);

CREATE TABLE plugin_drafts (
    plugin_id       uuid PRIMARY KEY REFERENCES plugins (id) ON DELETE CASCADE,
    files           jsonb NOT NULL,
    base_version_id uuid REFERENCES plugin_versions (id) ON DELETE SET NULL,
    updated_by      uuid,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    version         integer NOT NULL DEFAULT 1
);

CREATE TABLE plugin_storage (
    plugin_id  uuid NOT NULL REFERENCES plugins (id) ON DELETE CASCADE,
    channel_id uuid NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    key        text NOT NULL,
    value      jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (plugin_id, channel_id, key)
);

CREATE TABLE capability_results (
    channel_id        uuid NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    capability        text NOT NULL,
    ok                boolean NOT NULL,
    unsupported       boolean NOT NULL DEFAULT false,
    output            jsonb,
    error             text,
    plugin_version_id uuid,
    duration_ms       integer NOT NULL DEFAULT 0,
    fetched_at        timestamptz NOT NULL,
    PRIMARY KEY (channel_id, capability)
);

ALTER TABLE channels
    ADD COLUMN plugin_version_id uuid REFERENCES plugin_versions (id),
    ADD COLUMN plugin_config jsonb NOT NULL DEFAULT '{}'::jsonb;
CREATE INDEX channels_plugin_version_idx ON channels (plugin_version_id) WHERE plugin_version_id IS NOT NULL;

-- +goose Down
ALTER TABLE channels DROP COLUMN plugin_config, DROP COLUMN plugin_version_id;
DROP TABLE capability_results;
DROP TABLE plugin_storage;
DROP TABLE plugin_drafts;
DROP TABLE plugin_versions;
DROP TABLE plugins;
