-- +goose Up
CREATE TABLE control_settings (
 id INTEGER PRIMARY KEY CHECK (id = 1), config TEXT NOT NULL,
 secret TEXT NOT NULL DEFAULT '', version INTEGER NOT NULL DEFAULT 1,
 updated_by UUID REFERENCES users(id), updated_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE control_events (
 id UUID PRIMARY KEY, request_id TEXT NOT NULL, rule_id TEXT NOT NULL,
 user_id UUID NOT NULL, key_id UUID NOT NULL,
 channel_id UUID NOT NULL, model TEXT NOT NULL, dialect TEXT NOT NULL,
 reason TEXT NOT NULL, probability DOUBLE PRECISION, scores TEXT NOT NULL DEFAULT '[]',
 threshold DOUBLE PRECISION NOT NULL, review_probability DOUBLE PRECISION, review_threshold DOUBLE PRECISION, reviewed_at TIMESTAMPTZ,
 excerpt TEXT NOT NULL DEFAULT '', failure BOOLEAN NOT NULL DEFAULT FALSE,
 action TEXT NOT NULL, action_state TEXT NOT NULL DEFAULT 'pending', action_error TEXT NOT NULL DEFAULT '', created_at TIMESTAMPTZ NOT NULL,
 UNIQUE(request_id, rule_id)
);
CREATE INDEX control_events_user_idx ON control_events(user_id, created_at);
CREATE TABLE control_snapshots (
 id UUID PRIMARY KEY, user_id UUID NOT NULL REFERENCES users(id), request_id TEXT NOT NULL,
 model TEXT NOT NULL, dialect TEXT NOT NULL, payload TEXT NOT NULL,
 truncated BOOLEAN NOT NULL DEFAULT FALSE, created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX control_snapshots_user_idx ON control_snapshots(user_id,created_at);
ALTER TABLE control_events ADD COLUMN snapshot_id UUID REFERENCES control_snapshots(id) ON DELETE SET NULL;
CREATE INDEX control_events_snapshot_idx ON control_events(snapshot_id);
CREATE INDEX control_events_created_idx ON control_events(created_at);
-- +goose Down
DROP TABLE control_events;
DROP TABLE control_snapshots;
DROP TABLE control_settings;
