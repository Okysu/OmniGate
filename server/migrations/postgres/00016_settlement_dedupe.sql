-- +goose Up
-- Durable settlement (ADR-0010): a settlement that records usage counters
-- without a wallet charge (plan-covered, own / shared channels, billing not
-- enforced) marks its request here in the same transaction, so a retried or
-- replayed settlement never counts twice. Charged requests are deduplicated
-- by their ledger entry (ledger_entries_charge_ref_uidx); request logs by
-- their primary key (id, started_at). Rows are pruned after 60 days.
CREATE TABLE settled_requests (
    request_id  text PRIMARY KEY,
    settled_at  timestamptz NOT NULL
);
CREATE INDEX settled_requests_settled_idx ON settled_requests (settled_at);

-- +goose Down
DROP TABLE settled_requests;
