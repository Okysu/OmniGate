-- +goose Up
-- SQLite edition of postgres/00018_reset_cards.sql: quota reset cards
-- (docs/contracts/phase11-api.md §2).
CREATE TABLE reset_card_batches (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL CHECK (kind IN ('5h', 'weekly', 'both')),
    quantity    INTEGER NOT NULL CHECK (quantity BETWEEN 1 AND 100),
    recipients  INTEGER NOT NULL CHECK (recipients >= 0),
    target      TEXT NOT NULL CHECK (json_valid(target)),
    plan_ids    TEXT CHECK (plan_ids IS NULL OR json_valid(plan_ids)),
    expires_at  TEXT,
    note        TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
    created_by  TEXT NOT NULL REFERENCES users (id),
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    revoked_at  TEXT,
    revoked_by  TEXT REFERENCES users (id),
    CHECK (status <> 'revoked' OR revoked_at IS NOT NULL)
) STRICT;
CREATE INDEX reset_card_batches_created_idx ON reset_card_batches (created_at DESC, id DESC);

CREATE TABLE reset_cards (
    id               TEXT PRIMARY KEY,
    batch_id         TEXT NOT NULL REFERENCES reset_card_batches (id) ON DELETE CASCADE,
    user_id          TEXT NOT NULL REFERENCES users (id),
    kind             TEXT NOT NULL CHECK (kind IN ('5h', 'weekly', 'both')),
    expires_at       TEXT,
    status           TEXT NOT NULL DEFAULT 'available' CHECK (status IN ('available', 'used', 'revoked')),
    used_at          TEXT,
    subscription_id  TEXT REFERENCES subscriptions (id) ON DELETE SET NULL,
    revoked_at       TEXT,
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    CHECK (status <> 'used' OR used_at IS NOT NULL),
    CHECK (status <> 'revoked' OR revoked_at IS NOT NULL)
) STRICT;
CREATE INDEX reset_cards_user_status_idx ON reset_cards (user_id, status);
CREATE INDEX reset_cards_batch_idx ON reset_cards (batch_id, status);

-- +goose Down
DROP TABLE reset_cards;
DROP TABLE reset_card_batches;
