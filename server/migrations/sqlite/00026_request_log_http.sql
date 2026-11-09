-- +goose Up
ALTER TABLE request_logs ADD COLUMN request_method TEXT NOT NULL DEFAULT '';
ALTER TABLE request_logs ADD COLUMN request_path TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE request_logs DROP COLUMN request_path;
ALTER TABLE request_logs DROP COLUMN request_method;
