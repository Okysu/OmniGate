-- +goose Up
-- Per-model upstream protocol for openai channels: '' / 'chat' = Chat Completions,
-- 'responses' = OpenAI Responses (models such as gpt-6-* that only speak Responses).
ALTER TABLE channel_models ADD COLUMN upstream_protocol text NOT NULL DEFAULT ''
    CHECK (upstream_protocol IN ('', 'chat', 'responses'));

-- +goose Down
ALTER TABLE channel_models DROP COLUMN upstream_protocol;
