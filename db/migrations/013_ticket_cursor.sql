-- +goose Up

CREATE INDEX IF NOT EXISTS idx_bookings_confirmed_wa_queue
  ON bookings(status, is_whatsapp_sent, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_bookings_confirmed_created
  ON bookings(status, created_at DESC, id DESC);

-- +goose Down

DROP INDEX IF EXISTS idx_bookings_confirmed_created;
DROP INDEX IF EXISTS idx_bookings_confirmed_wa_queue;
