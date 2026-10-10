-- +goose Up
-- Round 11 (docs/contracts/phase11-api.md §2): quota reset cards. An
-- administrator issues a batch of cards to a set of users (materialized at
-- issue time: one row per card); a user spends a card on one of their live
-- subscriptions to reset its 5-hour and / or weekly windows. 'expired' is
-- derived (status 'available' and expires_at <= now) and never stored.
CREATE TABLE reset_card_batches (
    id          uuid PRIMARY KEY,
    kind        text NOT NULL CHECK (kind IN ('5h', 'weekly', 'both')),
    quantity    integer NOT NULL CHECK (quantity BETWEEN 1 AND 100),  -- cards per recipient
    recipients  integer NOT NULL CHECK (recipients >= 0),
    target      jsonb NOT NULL,                       -- the selector as issued, with name snapshots
    plan_ids    jsonb,                                -- uuid[]; NULL = usable on every plan
    expires_at  timestamptz,                          -- NULL = never expires
    note        text NOT NULL DEFAULT '',
    status      text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
    created_by  uuid NOT NULL REFERENCES users (id),
    created_at  timestamptz NOT NULL DEFAULT now(),
    revoked_at  timestamptz,
    revoked_by  uuid REFERENCES users (id),
    CHECK (status <> 'revoked' OR revoked_at IS NOT NULL)
);
CREATE INDEX reset_card_batches_created_idx ON reset_card_batches (created_at DESC, id DESC);

-- kind and expires_at are copied from the batch so a card is claimed with one
-- conditional UPDATE of its own row.
CREATE TABLE reset_cards (
    id               uuid PRIMARY KEY,
    batch_id         uuid NOT NULL REFERENCES reset_card_batches (id) ON DELETE CASCADE,
    user_id          uuid NOT NULL REFERENCES users (id),
    kind             text NOT NULL CHECK (kind IN ('5h', 'weekly', 'both')),
    expires_at       timestamptz,
    status           text NOT NULL DEFAULT 'available' CHECK (status IN ('available', 'used', 'revoked')),
    used_at          timestamptz,
    subscription_id  uuid REFERENCES subscriptions (id) ON DELETE SET NULL,
    revoked_at       timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    CHECK (status <> 'used' OR used_at IS NOT NULL),
    CHECK (status <> 'revoked' OR revoked_at IS NOT NULL)
);
CREATE INDEX reset_cards_user_status_idx ON reset_cards (user_id, status);
CREATE INDEX reset_cards_batch_idx ON reset_cards (batch_id, status);

-- +goose Down
DROP TABLE reset_cards;
DROP TABLE reset_card_batches;
