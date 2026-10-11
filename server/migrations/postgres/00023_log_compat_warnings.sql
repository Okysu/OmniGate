-- +goose Up
-- Fields the protocol converter dropped or could not convert for the request
-- (the X-OmniGate-Compat-Warnings header, comma-separated); NULL = none.
ALTER TABLE request_logs ADD COLUMN compat_warnings text;

-- +goose Down
ALTER TABLE request_logs DROP COLUMN compat_warnings;
