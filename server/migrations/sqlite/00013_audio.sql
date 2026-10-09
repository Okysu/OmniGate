-- +goose Up
-- SQLite edition of postgres/00013_audio.sql.

ALTER TABLE prices ADD COLUMN audio_input_per_m INTEGER CHECK (audio_input_per_m >= 0);
ALTER TABLE prices ADD COLUMN audio_output_per_m INTEGER CHECK (audio_output_per_m >= 0);
ALTER TABLE prices ADD COLUMN per_minute INTEGER NOT NULL DEFAULT 0 CHECK (per_minute >= 0);
ALTER TABLE prices ADD COLUMN per_m_characters INTEGER NOT NULL DEFAULT 0 CHECK (per_m_characters >= 0);

ALTER TABLE request_logs ADD COLUMN audio_seconds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE request_logs ADD COLUMN audio_input_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE request_logs ADD COLUMN audio_output_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE request_logs ADD COLUMN input_characters INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE request_logs DROP COLUMN input_characters;
ALTER TABLE request_logs DROP COLUMN audio_output_tokens;
ALTER TABLE request_logs DROP COLUMN audio_input_tokens;
ALTER TABLE request_logs DROP COLUMN audio_seconds;
ALTER TABLE prices DROP COLUMN per_m_characters;
ALTER TABLE prices DROP COLUMN per_minute;
ALTER TABLE prices DROP COLUMN audio_output_per_m;
ALTER TABLE prices DROP COLUMN audio_input_per_m;
