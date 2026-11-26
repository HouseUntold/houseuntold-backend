-- name: GetEventByID :one
SELECT id, title, starts_at, ends_at, location, status, created_at, venue_name, location_area, transit_info, advisory_notice, description, deleted_at, is_first_99_active
FROM events
WHERE id = $1 AND deleted_at IS NULL;

-- name: GetEventByIDForUpdate :one
SELECT id, title, starts_at, ends_at, location, status, created_at, venue_name, location_area, transit_info, advisory_notice, description, deleted_at, is_first_99_active
FROM events
WHERE id = $1 AND deleted_at IS NULL
FOR UPDATE;

-- name: CreateEvent :one
INSERT INTO events (
  title,
  starts_at,
  ends_at,
  location,
  venue_name,
  location_area,
  transit_info,
  advisory_notice,
  description,
  status
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id, title, starts_at, ends_at, location, status, created_at, venue_name, location_area, transit_info, advisory_notice, description, deleted_at, is_first_99_active;

-- name: UpdateEvent :one
UPDATE events
SET
  title = COALESCE(sqlc.narg(title)::text, title),
  starts_at = COALESCE(sqlc.narg(starts_at)::timestamptz, starts_at),
  ends_at = COALESCE(sqlc.narg(ends_at)::timestamptz, ends_at),
  location = COALESCE(sqlc.narg(location)::text, location),
  venue_name = COALESCE(sqlc.narg(venue_name)::text, venue_name),
  location_area = COALESCE(sqlc.narg(location_area)::text, location_area),
  transit_info = COALESCE(sqlc.narg(transit_info)::text, transit_info),
  advisory_notice = COALESCE(sqlc.narg(advisory_notice)::text, advisory_notice),
  description = COALESCE(sqlc.narg(description)::text, description),
  status = COALESCE(sqlc.narg(status)::event_status, status)
WHERE id = sqlc.arg(id) AND deleted_at IS NULL
RETURNING id, title, starts_at, ends_at, location, status, created_at, venue_name, location_area, transit_info, advisory_notice, description, deleted_at, is_first_99_active;

-- name: SoftDeleteEvent :one
UPDATE events
SET deleted_at = NOW()
WHERE id = $1 AND deleted_at IS NULL
RETURNING id, title, starts_at, ends_at, location, status, created_at, venue_name, location_area, transit_info, advisory_notice, description, deleted_at, is_first_99_active;

-- name: GetEventFirst99Active :one
SELECT is_first_99_active
FROM events
WHERE id = $1 AND deleted_at IS NULL;

-- name: SetEventFirst99Active :one
UPDATE events
SET is_first_99_active = $2
WHERE id = $1 AND deleted_at IS NULL
RETURNING id, is_first_99_active;

-- name: ListEventCategories :many
SELECT id, event_id, name, category, base_price, fnb_credit, entry_count, is_gender_strict
FROM ticket_categories
WHERE event_id = $1
ORDER BY base_price ASC, entry_count ASC;

-- name: GetCategoryByID :one
SELECT id, event_id, name, category, base_price, fnb_credit, entry_count, is_gender_strict
FROM ticket_categories
WHERE id = $1;

-- name: CreateTicketCategory :one
INSERT INTO ticket_categories (event_id, name, category, base_price, fnb_credit, entry_count, is_gender_strict)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, event_id, name, category, base_price, fnb_credit, entry_count, is_gender_strict;

-- name: UpdateTicketCategory :one
UPDATE ticket_categories
SET
  name = COALESCE(sqlc.narg(name)::text, name),
  category = COALESCE(sqlc.narg(category)::text, category),
  base_price = COALESCE(sqlc.narg(base_price)::integer, base_price),
  fnb_credit = COALESCE(sqlc.narg(fnb_credit)::integer, fnb_credit),
  entry_count = COALESCE(sqlc.narg(entry_count)::integer, entry_count),
  is_gender_strict = COALESCE(sqlc.narg(is_gender_strict)::boolean, is_gender_strict)
WHERE id = sqlc.arg(id)
RETURNING id, event_id, name, category, base_price, fnb_credit, entry_count, is_gender_strict;

-- name: UpdateTicketCategoryPrice :one
UPDATE ticket_categories
SET base_price = $2, fnb_credit = $3
WHERE id = $1
RETURNING id, event_id, name, category, base_price, fnb_credit, entry_count, is_gender_strict;

-- name: CountConfirmedFemaleStagByEvent :one
SELECT COALESCE(SUM(GREATEST(b.stag_female_count, CASE WHEN UPPER(tc.name) = 'STAG_FEMALE' THEN 1 ELSE 0 END)), 0)::bigint AS confirmed_count
FROM bookings b
  LEFT JOIN ticket_categories tc ON tc.id = b.category_id
WHERE b.event_id = $1 AND b.status = 'CONFIRMED';

-- name: ListPublicEvents :many
SELECT
  e.id,
  e.title,
  e.starts_at,
  e.ends_at,
  e.location,
  e.status,
  e.created_at,
  COUNT(tc.id)::bigint AS tier_count,
  COALESCE(MIN(tc.base_price), 0)::bigint AS min_price,
  COALESCE(MAX(tc.base_price), 0)::bigint AS max_price
FROM events e
  LEFT JOIN ticket_categories tc ON tc.event_id = e.id
WHERE e.deleted_at IS NULL
  AND e.status::text <> 'DRAFT'
  AND (sqlc.arg(status)::text = 'ALL' OR e.status::text = sqlc.arg(status))
GROUP BY e.id
ORDER BY e.starts_at DESC
LIMIT sqlc.arg(page_limit)
OFFSET sqlc.arg(page_offset);

-- name: ListAdminEvents :many
SELECT
  e.id,
  e.title,
  e.starts_at,
  e.ends_at,
  e.location,
  e.status,
  e.created_at,
  COUNT(b.id) FILTER (WHERE b.status = 'CONFIRMED')::bigint AS confirmed_bookings,
  COALESCE(SUM(b.total_amount) FILTER (WHERE b.status = 'CONFIRMED'), 0)::bigint AS ticket_revenue,
  COUNT(t.id) FILTER (WHERE t.is_checked_in = true)::bigint AS checked_in_tickets
FROM events e
  LEFT JOIN bookings b ON b.event_id = e.id
  LEFT JOIN tickets t ON t.booking_id = b.id
WHERE e.deleted_at IS NULL
GROUP BY e.id
ORDER BY e.starts_at DESC;

-- name: CreateArtist :one
INSERT INTO event_artists (event_id, name, category, description)
VALUES ($1, $2, $3, $4)
RETURNING id, event_id, name, category, description, created_at;

-- name: ListArtistsByEvent :many
SELECT id, event_id, name, category, description, created_at
FROM event_artists
WHERE event_id = $1
ORDER BY created_at ASC;

-- name: UpdateArtist :one
UPDATE event_artists
SET name = $2, category = $3, description = $4
WHERE id = $1 AND event_id = $5
RETURNING id, event_id, name, category, description, created_at;

-- name: DeleteArtist :exec
DELETE FROM event_artists
WHERE id = $1;
