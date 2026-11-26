-- +goose Up
ALTER TABLE staff_accounts ADD COLUMN token_version INT NOT NULL DEFAULT 0;
ALTER TABLE bookings ADD COLUMN idempotency_key VARCHAR(255);
CREATE UNIQUE INDEX idx_bookings_idempotency_key ON bookings (idempotency_key) WHERE idempotency_key IS NOT NULL;
ALTER TABLE ticket_guests ADD COLUMN free_shot_eligible BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
DROP INDEX IF EXISTS idx_bookings_idempotency_key;
ALTER TABLE ticket_guests DROP COLUMN IF EXISTS free_shot_eligible;
ALTER TABLE bookings DROP COLUMN IF EXISTS idempotency_key;
ALTER TABLE staff_accounts DROP COLUMN IF EXISTS token_version;
