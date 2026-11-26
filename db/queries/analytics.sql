-- name: GetEventBookingStats :one
SELECT
  COUNT(b.id)::bigint AS confirmed_bookings,
  COALESCE(SUM(b.total_amount), 0)::bigint AS ticket_revenue,
  COALESCE(SUM(t.initial_food_balance), 0)::bigint AS total_fnb_allocated,
  COALESCE(SUM(t.initial_food_balance - t.food_balance), 0)::bigint AS total_fnb_redeemed,
  COALESCE(SUM(t.food_balance), 0)::bigint AS total_fnb_remaining,
  COUNT(t.id) FILTER (WHERE t.is_checked_in = true)::bigint AS checked_in_tickets,
  COUNT(t.id)::bigint AS total_tickets
FROM bookings b
  INNER JOIN tickets t ON t.booking_id = b.id
WHERE b.event_id = $1 AND b.status = 'CONFIRMED';

-- name: GetEventBarRevenueStats :one
SELECT
  COUNT(bt.id)::bigint AS total_transactions,
  COALESCE(SUM(bt.total_bill), 0)::bigint AS total_bar_bill,
  COALESCE(SUM(bt.wallet_deducted), 0)::bigint AS total_wallet_deducted,
  COALESCE(SUM(bt.upi_delta_paid), 0)::bigint AS total_upi_delta
FROM bar_transactions bt
  INNER JOIN tickets t ON t.id = bt.ticket_id
WHERE t.event_id = $1;

-- name: GetEventGenderStats :one
SELECT
  COUNT(tg.id)::bigint AS total_guests,
  COUNT(tg.id) FILTER (WHERE tg.gender = 'MALE')::bigint AS male_guests,
  COUNT(tg.id) FILTER (WHERE tg.gender = 'FEMALE')::bigint AS female_guests,
  COUNT(tg.id) FILTER (WHERE tg.gender = 'OTHER')::bigint AS other_guests,
  COUNT(tg.id) FILTER (WHERE tg.free_shot_redeemed = true)::bigint AS free_shots_redeemed
FROM ticket_guests tg
  INNER JOIN tickets t ON t.id = tg.ticket_id
  INNER JOIN bookings b ON b.id = t.booking_id
WHERE t.event_id = $1 AND b.status = 'CONFIRMED';

-- name: GetV2GroupStatsByEvent :one
SELECT
  COALESCE(SUM(b.couple_count), 0)::bigint AS total_couples,
  COALESCE(SUM(b.stag_male_count), 0)::bigint AS total_stag_male,
  COALESCE(SUM(b.stag_female_count), 0)::bigint AS total_stag_female,
  COALESCE(SUM(b.free_tequila_redeemed_count), 0)::bigint AS total_tequila_redeemed
FROM bookings b
WHERE b.event_id = $1 AND b.status = 'CONFIRMED';

-- name: GetV2FinancialStatsByEvent :one
SELECT
  (
    SELECT COALESCE(SUM(t.initial_food_balance - t.food_balance), 0)::bigint
    FROM tickets t
    WHERE t.event_id = $1
  ) AS fnb_credit_used,
  (
    SELECT COALESCE(SUM(bt.cash_delta_paid), 0)::bigint
    FROM bar_transactions bt
      INNER JOIN tickets t ON t.id = bt.ticket_id
    WHERE t.event_id = $1
  ) AS extra_cash_collected,
  (
    SELECT COALESCE(SUM(bt.upi_delta_paid), 0)::bigint
    FROM bar_transactions bt
      INNER JOIN tickets t ON t.id = bt.ticket_id
    WHERE t.event_id = $1
  ) AS extra_upi_collected;

-- name: GetTopBarItemsByEvent :many
SELECT
  mi.name AS item_name,
  mi.category::text AS item_category,
  SUM(tli.quantity)::bigint AS quantity,
  tli.unit_price_at_sale,
  (SUM(tli.quantity) * tli.unit_price_at_sale)::bigint AS revenue
FROM transaction_line_items tli
  INNER JOIN menu_items mi ON mi.id = tli.menu_item_id
  INNER JOIN bar_transactions bt ON bt.id = tli.transaction_id
  INNER JOIN tickets t ON t.id = bt.ticket_id
WHERE t.event_id = $1
GROUP BY mi.id, mi.name, mi.category, tli.unit_price_at_sale
ORDER BY revenue DESC, mi.name ASC
LIMIT $2;

-- name: GetBarSalesByBartender :many
SELECT
  sa.id AS bartender_id,
  sa.username AS bartender_username,
  COUNT(bt.id)::bigint AS transactions,
  COALESCE(SUM(bt.total_bill), 0)::bigint AS total_bill,
  COALESCE(SUM(bt.wallet_deducted), 0)::bigint AS wallet_deducted,
  COALESCE(SUM(bt.upi_delta_paid), 0)::bigint AS delta_collected
FROM bar_transactions bt
  INNER JOIN tickets t ON t.id = bt.ticket_id
  INNER JOIN staff_accounts sa ON sa.id = bt.bartender_id
WHERE t.event_id = $1
GROUP BY sa.id, sa.username
ORDER BY total_bill DESC;

-- name: GetRecentGateEntries :many
SELECT
  t.id AS ticket_id,
  t.bar_code,
  t.checked_in_at,
  tg.name AS primary_guest,
  sa.username AS gatekeeper_username,
  tc.name AS category_name
FROM tickets t
  INNER JOIN bookings b ON b.id = t.booking_id
  INNER JOIN ticket_categories tc ON tc.id = b.category_id
  LEFT JOIN ticket_guests tg ON tg.ticket_id = t.id AND tg.is_primary_contact = true
  LEFT JOIN staff_accounts sa ON sa.id = t.checked_in_by
WHERE t.event_id = $1 AND t.is_checked_in = true
ORDER BY t.checked_in_at DESC
LIMIT $2;
