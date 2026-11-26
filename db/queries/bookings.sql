-- name: CreateBooking :one
INSERT INTO bookings (
  event_id,
  category_id,
  contact_phone,
  contact_email,
  total_amount,
  upi_transaction_id,
  status,
  idempotency_key
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING
  id,
  event_id,
  category_id,
  contact_phone,
  contact_email,
  total_amount,
  upi_transaction_id,
  status,
  verified_by,
  verified_at,
  rejection_reason,
  idempotency_key,
  created_at;

-- name: SubmitBookingUPITransactionID :one
UPDATE bookings
SET upi_transaction_id = $2
WHERE id = $1 AND status = 'PENDING' AND contact_phone = $3
RETURNING
  id,
  event_id,
  category_id,
  contact_phone,
  contact_email,
  total_amount,
  upi_transaction_id,
  status,
  verified_by,
  verified_at,
  rejection_reason,
  idempotency_key,
  created_at;

-- name: GetBookingByIDForUpdate :one
SELECT
  id,
  event_id,
  category_id,
  contact_phone,
  contact_email,
  total_amount,
  upi_transaction_id,
  status,
  verified_by,
  verified_at,
  rejection_reason,
  idempotency_key,
  created_at
FROM bookings
WHERE id = $1
FOR UPDATE;

-- name: ApproveBookingAtomic :one
UPDATE bookings
SET status = 'CONFIRMED', verified_by = $2, verified_at = NOW(), rejection_reason = NULL
WHERE id = $1 AND status = 'PENDING'
RETURNING
  id,
  event_id,
  category_id,
  contact_phone,
  contact_email,
  total_amount,
  upi_transaction_id,
  status,
  verified_by,
  verified_at,
  rejection_reason,
  idempotency_key,
  created_at;

-- name: RejectBookingAtomic :one
UPDATE bookings
SET status = 'FAILED', verified_by = $2, verified_at = NOW(), rejection_reason = $3
WHERE id = $1 AND status = 'PENDING'
RETURNING
  id,
  event_id,
  category_id,
  contact_phone,
  contact_email,
  total_amount,
  upi_transaction_id,
  status,
  verified_by,
  verified_at,
  rejection_reason,
  idempotency_key,
  created_at;

-- name: ListBookingsForVerification :many
SELECT
  b.id,
  b.event_id,
  b.category_id,
  b.contact_phone,
  b.contact_email,
  b.total_amount,
  b.upi_transaction_id,
  b.status,
  b.verified_by,
  b.verified_at,
  b.rejection_reason,
  b.idempotency_key,
  b.created_at,
  tc.name AS category_name,
  tc.category AS category_tier,
  tc.base_price AS tier_base_price,
  tc.fnb_credit AS tier_fnb_credit,
  tc.entry_count,
  e.title AS event_title,
  t.id AS ticket_id,
  t.bar_code
FROM bookings b
  INNER JOIN ticket_categories tc ON tc.id = b.category_id
  INNER JOIN events e ON e.id = b.event_id
  INNER JOIN tickets t ON t.booking_id = b.id
WHERE b.status = sqlc.arg(status)
  AND e.deleted_at IS NULL
  AND (sqlc.arg(status)::booking_status != 'PENDING' OR b.upi_transaction_id IS NOT NULL)
  AND (sqlc.arg(event_id)::uuid = '00000000-0000-0000-0000-000000000000'::uuid OR b.event_id = sqlc.arg(event_id))
ORDER BY b.created_at DESC
LIMIT sqlc.arg(page_limit)
OFFSET sqlc.arg(page_offset);

-- name: CreateTicket :one
INSERT INTO tickets (
  id,
  booking_id,
  event_id,
  qr_token_digest,
  bar_code,
  food_balance,
  initial_food_balance,
  is_checked_in
)
VALUES ($1, $2, $3, $4, $5, $6, $7, false)
RETURNING
  id,
  booking_id,
  event_id,
  qr_token_digest,
  bar_code,
  food_balance,
  initial_food_balance,
  is_checked_in,
  checked_in_at;

-- name: CreateTicketGuest :one
INSERT INTO ticket_guests (ticket_id, name, age, gender, is_primary_contact, free_shot_eligible, free_shot_redeemed)
VALUES ($1, $2, $3, $4, $5, $6, false)
RETURNING id, ticket_id, name, age, gender, is_primary_contact, free_shot_eligible, free_shot_redeemed;

-- name: GetTicketByBookingID :one
SELECT
  id,
  booking_id,
  event_id,
  qr_token_digest,
  bar_code,
  food_balance,
  initial_food_balance,
  is_checked_in,
  checked_in_at
FROM tickets
WHERE booking_id = $1;

-- name: GetTicketDetailsByID :one
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
  b.total_amount,
  b.upi_transaction_id,
  b.contact_phone,
  b.contact_email,
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
WHERE t.id = $1 AND e.deleted_at IS NULL;

-- name: ListGuestsByTicketID :many
SELECT id, ticket_id, name, age, gender, is_primary_contact, free_shot_eligible, free_shot_redeemed
FROM ticket_guests
WHERE ticket_id = $1
ORDER BY is_primary_contact DESC, name ASC;

-- name: SearchConfirmedTickets :many
SELECT t.id, b.created_at AS cursor_created_at, b.id AS cursor_id
FROM tickets t
  INNER JOIN bookings b ON b.id = t.booking_id
  INNER JOIN events e ON e.id = t.event_id
WHERE b.status = 'CONFIRMED'
  AND e.deleted_at IS NULL
  AND (
    sqlc.arg(cursor_id)::uuid = '00000000-0000-0000-0000-000000000000'::uuid
    OR (b.created_at, b.id) < (sqlc.arg(cursor_created_at)::timestamptz, sqlc.arg(cursor_id)::uuid)
  )
  AND (
    sqlc.arg(search_query)::text = ''
    OR b.contact_phone ILIKE '%' || sqlc.arg(search_query)::text || '%'
    OR COALESCE(b.upi_transaction_id, '') ILIKE '%' || sqlc.arg(search_query)::text || '%'
    OR EXISTS (
      SELECT 1
      FROM ticket_guests tg
      WHERE tg.ticket_id = t.id AND tg.name ILIKE '%' || sqlc.arg(search_query)::text || '%'
    )
  )
ORDER BY b.created_at DESC, b.id DESC
LIMIT sqlc.arg(page_limit);

-- name: UpdateBookingPaymentQRData :one
UPDATE bookings
SET payment_qr_data = $2
WHERE id = $1
RETURNING *;

-- name: MarkBookingWhatsAppSent :one
UPDATE bookings
SET is_whatsapp_sent = true
WHERE id = $1
RETURNING id, is_whatsapp_sent;

-- name: GetBookingIDByTicketID :one
SELECT booking_id
FROM tickets
WHERE id = $1;

-- name: GetTicketBookingDetails :one
SELECT
  b.id AS booking_id,
  b.contact_phone,
  b.upi_transaction_id,
  b.is_whatsapp_sent,
  b.couple_count,
  b.stag_male_count,
  b.stag_female_count,
  b.free_tequila_eligible_count,
  b.free_tequila_redeemed_count,
  COALESCE(sr.section, tc.category, tc.name) AS section,
  COALESCE(sr.table_size, 0)::int AS seat_table_size,
  COALESCE(sr.table_number, 0)::int AS seat_table_number,
  t.bar_code
FROM tickets t
  INNER JOIN bookings b ON b.id = t.booking_id
  INNER JOIN ticket_categories tc ON tc.id = b.category_id
  LEFT JOIN event_seat_reservations sr ON sr.ticket_id = t.id
WHERE t.id = $1;

-- name: SearchPendingWhatsAppTickets :many
SELECT t.id, b.created_at AS cursor_created_at, b.id AS cursor_id
FROM tickets t
  INNER JOIN bookings b ON b.id = t.booking_id
  INNER JOIN events e ON e.id = t.event_id
WHERE b.status = 'CONFIRMED'
  AND b.is_whatsapp_sent = false
  AND e.deleted_at IS NULL
  AND (
    sqlc.arg(cursor_id)::uuid = '00000000-0000-0000-0000-000000000000'::uuid
    OR (b.created_at, b.id) < (sqlc.arg(cursor_created_at)::timestamptz, sqlc.arg(cursor_id)::uuid)
  )
  AND (
    sqlc.arg(search_query)::text = ''
    OR b.contact_phone ILIKE '%' || sqlc.arg(search_query)::text || '%'
    OR COALESCE(b.upi_transaction_id, '') ILIKE '%' || sqlc.arg(search_query)::text || '%'
    OR EXISTS (
      SELECT 1
      FROM ticket_guests tg
      WHERE tg.ticket_id = t.id AND tg.name ILIKE '%' || sqlc.arg(search_query)::text || '%'
    )
  )
ORDER BY b.created_at DESC, b.id DESC
LIMIT sqlc.arg(page_limit);

-- name: LookupPublicTickets :many
SELECT t.id
FROM tickets t
  INNER JOIN bookings b ON b.id = t.booking_id
  INNER JOIN events e ON e.id = t.event_id
WHERE e.deleted_at IS NULL
  AND (
    t.id::text = sqlc.arg(search_query)::text
    OR b.contact_phone = sqlc.arg(search_query)::text
    OR COALESCE(b.upi_transaction_id, '') = sqlc.arg(search_query)::text
  )
ORDER BY b.created_at DESC, b.id DESC
LIMIT 10;

-- name: SetBookingGroupCounts :exec
UPDATE bookings
SET couple_count = $2, stag_male_count = $3, stag_female_count = $4, free_tequila_eligible_count = $5
WHERE id = $1;

-- name: SetBookingFreeTequilaEligible :exec
UPDATE bookings
SET free_tequila_eligible_count = $2
WHERE id = $1;

-- name: IncrementBookingTequilaRedeemed :one
UPDATE bookings
SET free_tequila_redeemed_count = free_tequila_redeemed_count + $2
WHERE id = $1 AND free_tequila_redeemed_count + $2 <= free_tequila_eligible_count
RETURNING id, free_tequila_eligible_count, free_tequila_redeemed_count;
