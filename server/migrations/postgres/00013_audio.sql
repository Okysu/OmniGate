-- +goose Up
-- Round 6 (continued, docs/contracts/phase9-api.md §1): audio endpoint
-- pricing and metering.

-- §1.1: price per 1M audio input / output tokens (NULL = billed at
-- input_per_m / output_per_m), per minute of input audio (prorated per
-- second) and per 1M speech input characters (0 = not charged). Nano units
-- like the other amounts.
ALTER TABLE prices ADD COLUMN audio_input_per_m bigint CHECK (audio_input_per_m >= 0);
ALTER TABLE prices ADD COLUMN audio_output_per_m bigint CHECK (audio_output_per_m >= 0);
ALTER TABLE prices ADD COLUMN per_minute bigint NOT NULL DEFAULT 0 CHECK (per_minute >= 0);
ALTER TABLE prices ADD COLUMN per_m_characters bigint NOT NULL DEFAULT 0 CHECK (per_m_characters >= 0);

-- §1.1: billed seconds of input audio, the audio parts of input_tokens /
-- output_tokens, and the input characters of a speech request.
ALTER TABLE request_logs ADD COLUMN audio_seconds bigint NOT NULL DEFAULT 0;
ALTER TABLE request_logs ADD COLUMN audio_input_tokens bigint NOT NULL DEFAULT 0;
ALTER TABLE request_logs ADD COLUMN audio_output_tokens bigint NOT NULL DEFAULT 0;
ALTER TABLE request_logs ADD COLUMN input_characters bigint NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE request_logs DROP COLUMN input_characters;
ALTER TABLE request_logs DROP COLUMN audio_output_tokens;
ALTER TABLE request_logs DROP COLUMN audio_input_tokens;
ALTER TABLE request_logs DROP COLUMN audio_seconds;
ALTER TABLE prices DROP COLUMN per_m_characters;
ALTER TABLE prices DROP COLUMN per_minute;
ALTER TABLE prices DROP COLUMN audio_output_per_m;
ALTER TABLE prices DROP COLUMN audio_input_per_m;
