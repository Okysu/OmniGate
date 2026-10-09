-- +goose Up
-- Custom-protocol channels (docs/contracts/phase9-api.md §2): channel type
-- "custom" for channels whose plugin implements the upstream protocol.
ALTER TABLE channels DROP CONSTRAINT channels_type_check;
ALTER TABLE channels ADD CONSTRAINT channels_type_check CHECK (type IN ('openai', 'anthropic', 'custom'));

-- +goose Down
-- Custom channels cannot exist without the type: they are kept as openai
-- channels (unusable until switched to a plugin version that inherits a protocol).
UPDATE channels SET type = 'openai' WHERE type = 'custom';
ALTER TABLE channels DROP CONSTRAINT channels_type_check;
ALTER TABLE channels ADD CONSTRAINT channels_type_check CHECK (type IN ('openai', 'anthropic'));
