-- +goose Up
-- SQLite edition of postgres/00011_images_users.sql.

ALTER TABLE prices ADD COLUMN per_image INTEGER NOT NULL DEFAULT 0 CHECK (per_image >= 0);
ALTER TABLE prices ADD COLUMN image_input_per_m INTEGER CHECK (image_input_per_m >= 0);

ALTER TABLE request_logs ADD COLUMN image_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE request_logs ADD COLUMN image_input_tokens INTEGER NOT NULL DEFAULT 0;

ALTER TABLE users ADD COLUMN disabled_reason TEXT;
ALTER TABLE users ADD COLUMN disabled_until TEXT;
CREATE INDEX users_disabled_until_idx ON users (disabled_until)
    WHERE status = 'disabled' AND disabled_until IS NOT NULL;

-- +goose Down
DROP INDEX users_disabled_until_idx;
ALTER TABLE users DROP COLUMN disabled_until;
ALTER TABLE users DROP COLUMN disabled_reason;
ALTER TABLE request_logs DROP COLUMN image_input_tokens;
ALTER TABLE request_logs DROP COLUMN image_count;
ALTER TABLE prices DROP COLUMN image_input_per_m;
ALTER TABLE prices DROP COLUMN per_image;
