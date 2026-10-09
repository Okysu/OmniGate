-- +goose Up
-- User-to-user channel shares require the recipient's acceptance
-- (docs/contracts/phase5-api.md §5): only accepted shares make a channel
-- usable. Existing shares were already effective and stay accepted.
ALTER TABLE channel_shares
    ADD COLUMN status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'declined')),
    ADD COLUMN responded_at timestamptz;
UPDATE channel_shares SET status = 'accepted', responded_at = created_at;
CREATE INDEX channel_shares_user_status_idx ON channel_shares (user_id, status);

-- +goose Down
-- The previous schema treats every row as an effective share: invitations
-- that were not accepted must not become usable.
DELETE FROM channel_shares WHERE status <> 'accepted';
DROP INDEX channel_shares_user_status_idx;
ALTER TABLE channel_shares DROP COLUMN responded_at, DROP COLUMN status;
