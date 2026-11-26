-- +goose Up

ALTER TABLE ticket_guests DROP CONSTRAINT IF EXISTS ticket_guests_age_check;
ALTER TABLE ticket_guests ALTER COLUMN age DROP NOT NULL;
ALTER TABLE ticket_guests ADD CONSTRAINT ticket_guests_age_check CHECK (age IS NULL OR (age >= 18 AND age <= 120));

ALTER TABLE bookings ADD COLUMN IF NOT EXISTS is_whatsapp_sent BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE bookings ADD COLUMN IF NOT EXISTS free_tequila_eligible_count INT NOT NULL DEFAULT 0 CHECK (
  free_tequila_eligible_count >= 0
);
ALTER TABLE bookings ADD COLUMN IF NOT EXISTS free_tequila_redeemed_count INT NOT NULL DEFAULT 0 CHECK (
  free_tequila_redeemed_count >= 0
);
ALTER TABLE bookings ADD CONSTRAINT chk_bookings_tequila_counts CHECK (
  free_tequila_redeemed_count <= free_tequila_eligible_count
);
ALTER TABLE bookings ADD COLUMN IF NOT EXISTS couple_count INT NOT NULL DEFAULT 0 CHECK (couple_count >= 0);
ALTER TABLE bookings ADD COLUMN IF NOT EXISTS stag_male_count INT NOT NULL DEFAULT 0 CHECK (stag_male_count >= 0);
ALTER TABLE bookings ADD COLUMN IF NOT EXISTS stag_female_count INT NOT NULL DEFAULT 0 CHECK (stag_female_count >= 0);

ALTER TABLE bar_transactions ADD COLUMN IF NOT EXISTS cash_delta_paid INT NOT NULL DEFAULT 0 CHECK (
  cash_delta_paid >= 0
);
ALTER TABLE events ADD COLUMN IF NOT EXISTS is_first_99_active BOOLEAN NOT NULL DEFAULT true;

CREATE INDEX IF NOT EXISTS idx_bookings_event_whatsapp ON bookings(event_id, is_whatsapp_sent);

-- +goose Down

DROP INDEX IF EXISTS idx_bookings_event_whatsapp;

ALTER TABLE events DROP COLUMN IF EXISTS is_first_99_active;
ALTER TABLE bar_transactions DROP COLUMN IF EXISTS cash_delta_paid;

ALTER TABLE bookings DROP CONSTRAINT IF EXISTS chk_bookings_tequila_counts;
ALTER TABLE bookings DROP COLUMN IF EXISTS stag_female_count;
ALTER TABLE bookings DROP COLUMN IF EXISTS stag_male_count;
ALTER TABLE bookings DROP COLUMN IF EXISTS couple_count;
ALTER TABLE bookings DROP COLUMN IF EXISTS free_tequila_redeemed_count;
ALTER TABLE bookings DROP COLUMN IF EXISTS free_tequila_eligible_count;
ALTER TABLE bookings DROP COLUMN IF EXISTS is_whatsapp_sent;

ALTER TABLE ticket_guests DROP CONSTRAINT IF EXISTS ticket_guests_age_check;
UPDATE ticket_guests
SET age = 21
WHERE age IS NULL;
ALTER TABLE ticket_guests ALTER COLUMN age SET NOT NULL;
ALTER TABLE ticket_guests ADD CONSTRAINT ticket_guests_age_check CHECK (age >= 21 AND age <= 120);
