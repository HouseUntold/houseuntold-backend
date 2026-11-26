-- name: GetGroupTicketCategory :one
SELECT id, event_id, name, category, base_price, fnb_credit, entry_count, is_gender_strict
FROM ticket_categories
WHERE event_id = $1 AND category = $2 AND entry_count = $3 AND name LIKE 'GROUP_%';

-- name: UpdateGroupTicketCategory :one
UPDATE ticket_categories
SET base_price = $2, fnb_credit = $3, is_gender_strict = false
WHERE id = $1
RETURNING id, event_id, name, category, base_price, fnb_credit, entry_count, is_gender_strict;

-- name: UpsertSeatingInventory :one
INSERT INTO event_seating_inventory (event_id, section, table_size, total_tables, ticket_category_id, updated_at)
VALUES ($1, $2, $3, $4, $5, now())
ON CONFLICT (event_id, section, table_size) DO UPDATE
SET total_tables = EXCLUDED.total_tables, ticket_category_id = EXCLUDED.ticket_category_id, updated_at = now()
RETURNING id, event_id, section, table_size, total_tables, ticket_category_id, created_at, updated_at;

-- name: GetSeatingInventoryForUpdate :one
SELECT id, event_id, section, table_size, total_tables, ticket_category_id, created_at, updated_at
FROM event_seating_inventory
WHERE event_id = $1 AND section = $2 AND table_size = $3
FOR UPDATE;

-- name: AcquireGroupSeatReservation :one
WITH inv AS (
  SELECT i.event_id, i.section, i.table_size, i.total_tables
  FROM event_seating_inventory i
  WHERE i.event_id = $1 AND i.section = $2 AND i.table_size = $3
  FOR UPDATE OF i
),
used AS (
  SELECT COUNT(r.id)::int AS n
  FROM event_seat_reservations r
  WHERE r.event_id = $1 AND r.section = $2 AND r.table_size = $3
),
next_table AS (
  SELECT used.n + 1 AS table_number
  FROM used
)
INSERT INTO event_seat_reservations (event_id, section, table_size, table_number, booking_id, ticket_id)
SELECT inv.event_id, inv.section, inv.table_size, next_table.table_number, $4, $5
FROM inv
  CROSS JOIN next_table
WHERE next_table.table_number <= inv.total_tables
RETURNING id, event_id, section, table_size, table_number, booking_id, ticket_id, created_at;

-- name: ListEventSeatInventory :many
SELECT
  i.section,
  i.table_size,
  i.total_tables,
  COUNT(r.id)::int AS reserved_tables,
  (i.total_tables - COUNT(r.id)::int)::int AS available_tables,
  c.base_price AS table_total,
  c.fnb_credit,
  c.id AS ticket_category_id
FROM event_seating_inventory i
  LEFT JOIN event_seat_reservations r
    ON r.event_id = i.event_id
    AND r.section = i.section
    AND r.table_size = i.table_size
  LEFT JOIN ticket_categories c ON c.id = i.ticket_category_id
WHERE i.event_id = $1
GROUP BY i.section, i.table_size, i.total_tables, c.base_price, c.fnb_credit, c.id
ORDER BY i.section ASC, i.table_size ASC;

-- name: ListEventSeats :many
SELECT
  i.section,
  i.table_size,
  g.n::int AS table_number,
  (r.id IS NOT NULL)::bool AS is_booked,
  c.base_price AS table_total,
  c.fnb_credit
FROM event_seating_inventory i
  CROSS JOIN generate_series(1, i.total_tables) AS g(n)
  LEFT JOIN event_seat_reservations r
    ON r.event_id = i.event_id
    AND r.section = i.section
    AND r.table_size = i.table_size
    AND r.table_number = g.n
  LEFT JOIN ticket_categories c ON c.id = i.ticket_category_id
WHERE i.event_id = $1
ORDER BY i.section ASC, i.table_size ASC, g.n ASC;

-- name: GetSeatReservationByBookingID :one
SELECT id, event_id, section, table_size, table_number, booking_id, ticket_id, created_at
FROM event_seat_reservations
WHERE booking_id = $1;

-- name: ListSeatInventoryByEventIDs :many
SELECT
  i.event_id,
  i.section,
  i.table_size,
  i.total_tables,
  COUNT(r.id)::int AS reserved_tables,
  (i.total_tables - COUNT(r.id)::int)::int AS available_tables,
  c.base_price AS table_total,
  c.fnb_credit,
  c.id AS ticket_category_id
FROM event_seating_inventory i
  LEFT JOIN event_seat_reservations r
    ON r.event_id = i.event_id
    AND r.section = i.section
    AND r.table_size = i.table_size
  LEFT JOIN ticket_categories c ON c.id = i.ticket_category_id
WHERE i.event_id = ANY($1::uuid[])
GROUP BY i.event_id, i.section, i.table_size, i.total_tables, c.base_price, c.fnb_credit, c.id
ORDER BY i.event_id, i.section ASC, i.table_size ASC;

-- name: ReleaseSeatReservationByBookingID :exec
DELETE FROM event_seat_reservations
WHERE booking_id = $1;
