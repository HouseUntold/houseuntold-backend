-- +goose Up
UPDATE bookings
SET upi_transaction_id = regexp_replace(upi_transaction_id, '\D', '', 'g')
WHERE upi_transaction_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_bookings_upi_txn_id ON bookings (
  upi_transaction_id
) WHERE upi_transaction_id IS NOT NULL;

-- +goose Down
