-- name: GetTicketDetailsByQRDigest :one
SELECT
  t.id,
  t.booking_id,
  t.event_id,
  t.qr_token_digest,
  t.bar_code,
  t.food_balance,
  t.initial_food_balance,
  t.is_checked_in,
  t.checked_in_at,
  b.status AS booking_status,
  tc.name AS category_name,
  tc.category AS category_tier,
  tc.entry_count,
  e.title AS event_title,
  e.starts_at AS event_starts_at,
  e.ends_at AS event_ends_at,
  e.location AS event_location
FROM tickets t
  INNER JOIN bookings b ON b.id = t.booking_id
  INNER JOIN ticket_categories tc ON tc.id = b.category_id
  INNER JOIN events e ON e.id = t.event_id
WHERE t.qr_token_digest = $1 AND e.deleted_at IS NULL;

-- name: CheckInTicketAtomic :one
UPDATE tickets t
SET is_checked_in = true, checked_in_at = NOW(), checked_in_by = $2
FROM bookings b
WHERE t.qr_token_digest = $1 AND t.booking_id = b.id AND b.status = 'CONFIRMED' AND t.is_checked_in = false
RETURNING
  t.id,
  t.booking_id,
  t.event_id,
  t.qr_token_digest,
  t.bar_code,
  t.food_balance,
  t.initial_food_balance,
  t.is_checked_in,
  t.checked_in_at;
