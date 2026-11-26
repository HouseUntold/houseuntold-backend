-- name: ListActiveMenuItems :many
SELECT id, name, category, unit_price, is_active, serving_label
FROM menu_items
WHERE is_active = true
ORDER BY category ASC, name ASC;

-- name: CreateMenuItem :one
INSERT INTO menu_items (name, category, unit_price, is_active, serving_label)
VALUES ($1, $2, $3, true, $4)
RETURNING id, name, category, unit_price, is_active, serving_label;

-- name: UpdateMenuItem :one
UPDATE menu_items
SET name = $2, category = $3, unit_price = $4, serving_label = $5
WHERE id = $1 AND is_active = true
RETURNING id, name, category, unit_price, is_active, serving_label;

-- name: DeactivateMenuItem :one
UPDATE menu_items
SET is_active = false
WHERE id = $1 AND is_active = true
RETURNING id, name, category, unit_price, is_active, serving_label;

-- name: GetActiveMenuItemsByIDs :many
SELECT id, name, category, unit_price, is_active, serving_label
FROM menu_items
WHERE id = ANY($1::uuid[]) AND is_active = true;

-- name: GetTicketDetailsByBarCode :one
SELECT
  t.id,
  t.booking_id,
  t.event_id,
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
  e.ends_at AS event_ends_at
FROM tickets t
  INNER JOIN bookings b ON b.id = t.booking_id
  INNER JOIN ticket_categories tc ON tc.id = b.category_id
  INNER JOIN events e ON e.id = t.event_id
WHERE t.bar_code = $1 AND e.deleted_at IS NULL;

-- name: GetTicketForUpdateByBarCode :one
SELECT
  t.id,
  t.booking_id,
  t.event_id,
  t.bar_code,
  t.food_balance,
  t.initial_food_balance,
  t.is_checked_in,
  b.status AS booking_status,
  e.ends_at AS event_ends_at
FROM tickets t
  INNER JOIN bookings b ON b.id = t.booking_id
  INNER JOIN events e ON e.id = t.event_id
WHERE t.bar_code = $1 AND e.deleted_at IS NULL
FOR UPDATE OF t;

-- name: DeductTicketFoodBalanceAtomic :one
UPDATE tickets
SET food_balance = food_balance - $2
WHERE id = $1 AND food_balance >= $2
RETURNING id, food_balance, initial_food_balance;

-- name: GetBarTransactionByIdempotencyKey :one
SELECT
  id,
  ticket_id,
  bartender_id,
  idempotency_key,
  total_bill,
  wallet_deducted,
  upi_delta_paid,
  cash_delta_paid,
  delta_payment_method,
  delta_collected_at,
  balance_before,
  balance_after,
  created_at
FROM bar_transactions
WHERE idempotency_key = $1;

-- name: CreateBarTransaction :one
INSERT INTO bar_transactions (
  ticket_id,
  bartender_id,
  idempotency_key,
  total_bill,
  wallet_deducted,
  upi_delta_paid,
  cash_delta_paid,
  delta_payment_method,
  delta_collected_at,
  balance_before,
  balance_after
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING
  id,
  ticket_id,
  bartender_id,
  idempotency_key,
  total_bill,
  wallet_deducted,
  upi_delta_paid,
  cash_delta_paid,
  delta_payment_method,
  delta_collected_at,
  balance_before,
  balance_after,
  created_at;

-- name: CreateTransactionLineItem :one
INSERT INTO transaction_line_items (transaction_id, menu_item_id, quantity, unit_price_at_sale)
VALUES ($1, $2, $3, $4)
RETURNING id, transaction_id, menu_item_id, quantity, unit_price_at_sale;

-- name: ListBarTransactionsByTicketID :many
SELECT
  bt.id,
  bt.ticket_id,
  bt.bartender_id,
  sa.username AS bartender_username,
  bt.idempotency_key,
  bt.total_bill,
  bt.wallet_deducted,
  bt.upi_delta_paid,
  bt.cash_delta_paid,
  bt.delta_payment_method,
  bt.delta_collected_at,
  bt.balance_before,
  bt.balance_after,
  bt.created_at
FROM bar_transactions bt
  INNER JOIN staff_accounts sa ON sa.id = bt.bartender_id
WHERE bt.ticket_id = $1
ORDER BY bt.created_at DESC
LIMIT $2;

-- name: ListLineItemsByTransactionIDs :many
SELECT tli.id, tli.transaction_id, tli.menu_item_id, mi.name AS menu_item_name, tli.quantity, tli.unit_price_at_sale
FROM transaction_line_items tli
  INNER JOIN menu_items mi ON mi.id = tli.menu_item_id
WHERE tli.transaction_id = ANY($1::uuid[])
ORDER BY tli.id ASC;

-- name: RedeemGuestFreeShotAtomic :one
UPDATE ticket_guests
SET free_shot_redeemed = true
WHERE id = $1 AND ticket_id = $2 AND gender = 'FEMALE' AND free_shot_eligible = true AND free_shot_redeemed = false
RETURNING id, ticket_id, name, gender, free_shot_redeemed;

-- name: GetBookingTequilaCountsByTicketID :one
SELECT b.id AS booking_id, b.couple_count, b.stag_female_count, b.free_tequila_eligible_count, b.free_tequila_redeemed_count
FROM bookings b
  INNER JOIN tickets t ON t.booking_id = b.id
WHERE t.id = $1;

-- name: RedeemBookingTequilaShotsAtomic :one
UPDATE bookings
SET free_tequila_redeemed_count = free_tequila_redeemed_count + $2
WHERE id = $1 AND free_tequila_redeemed_count + $2 <= free_tequila_eligible_count
RETURNING id, free_tequila_eligible_count, free_tequila_redeemed_count;
