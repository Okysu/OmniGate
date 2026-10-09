-- +goose Up
-- SQLite edition of postgres/00015_share_acceptance.sql: user-to-user channel
-- shares require acceptance (docs/contracts/phase5-api.md §5). Existing
-- shares were already effective and stay accepted.
ALTER TABLE channel_shares ADD COLUMN status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'declined'));
ALTER TABLE channel_shares ADD COLUMN responded_at TEXT;
UPDATE channel_shares SET status = 'accepted', responded_at = created_at;
CREATE INDEX channel_shares_user_status_idx ON channel_shares (user_id, status);

-- +goose Down
-- The previous schema treats every row as an effective share: invitations
-- that were not accepted must not become usable.
DELETE FROM channel_shares WHERE status <> 'accepted';
DROP INDEX channel_shares_user_status_idx;
ALTER TABLE channel_shares DROP COLUMN responded_at;
ALTER TABLE channel_shares DROP COLUMN status;
