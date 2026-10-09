-- +goose Up
-- Round 6 (continued, docs/contracts/phase7-api.md): image endpoint pricing and
-- metering, and the reason / end of a user's suspension.

-- §1.1: price per output image (0 = not charged) and per 1M image input
-- tokens (NULL = billed at input_per_m). Nano units like the other amounts.
ALTER TABLE prices ADD COLUMN per_image bigint NOT NULL DEFAULT 0 CHECK (per_image >= 0);
ALTER TABLE prices ADD COLUMN image_input_per_m bigint CHECK (image_input_per_m >= 0);

-- §1.1: output images of an image request and the image part of input_tokens.
ALTER TABLE request_logs ADD COLUMN image_count integer NOT NULL DEFAULT 0;
ALTER TABLE request_logs ADD COLUMN image_input_tokens bigint NOT NULL DEFAULT 0;

-- §2.1: why the user is disabled and when the account is enabled again
-- automatically (NULL = permanent). Both are cleared when the user is enabled.
ALTER TABLE users ADD COLUMN disabled_reason text;
ALTER TABLE users ADD COLUMN disabled_until timestamptz;
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
