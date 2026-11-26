-- name: UpsertCheckoutLeadAttempt :exec
INSERT INTO checkout_leads (
  idempotency_key, event_id, category_id, contact_phone, whatsapp_number, contact_email,
  primary_name, primary_age, gender, selection_json, upi_transaction_id, status, error_message, updated_at
) VALUES (
  sqlc.arg(idempotency_key),
  sqlc.arg(event_id),
  sqlc.arg(category_id),
  sqlc.arg(contact_phone),
  sqlc.arg(whatsapp_number),
  sqlc.arg(contact_email),
  sqlc.arg(primary_name),
  sqlc.arg(primary_age),
  sqlc.arg(gender),
  sqlc.arg(selection_json)::jsonb,
  sqlc.arg(upi_transaction_id),
  'ATTEMPTED',
  NULL,
  NOW()
)
ON CONFLICT (idempotency_key) DO UPDATE SET
  event_id = EXCLUDED.event_id,
  category_id = EXCLUDED.category_id,
  contact_phone = EXCLUDED.contact_phone,
  whatsapp_number = EXCLUDED.whatsapp_number,
  contact_email = EXCLUDED.contact_email,
  primary_name = EXCLUDED.primary_name,
  primary_age = EXCLUDED.primary_age,
  gender = EXCLUDED.gender,
  selection_json = EXCLUDED.selection_json,
  upi_transaction_id = EXCLUDED.upi_transaction_id,
  status = CASE WHEN checkout_leads.status = 'CONFIRMED' THEN checkout_leads.status ELSE 'ATTEMPTED' END,
  error_message = NULL,
  updated_at = NOW();

-- name: MarkCheckoutLeadResult :exec
UPDATE checkout_leads
SET booking_id = sqlc.arg(booking_id),
    status = sqlc.arg(status),
    upi_transaction_id = COALESCE(NULLIF(sqlc.arg(upi_transaction_id)::text, ''), upi_transaction_id),
    error_message = sqlc.arg(error_message),
    updated_at = NOW()
WHERE idempotency_key = sqlc.arg(idempotency_key);

-- name: MarkCheckoutLeadUTR :exec
UPDATE checkout_leads
SET upi_transaction_id = sqlc.arg(upi_transaction_id),
    status = CASE WHEN status = 'CONFIRMED' THEN status ELSE 'UTR_SUBMITTED' END,
    error_message = NULL,
    updated_at = NOW()
WHERE booking_id = sqlc.arg(booking_id);

-- name: ListCheckoutLeads :many
SELECT
  cl.id,
  cl.idempotency_key,
  cl.event_id,
  COALESCE(e.title, '') AS event_title,
  cl.category_id,
  COALESCE(tc.name, '') AS category_name,
  cl.contact_phone,
  cl.whatsapp_number,
  cl.contact_email,
  cl.primary_name,
  cl.primary_age,
  cl.gender,
  cl.selection_json,
  cl.upi_transaction_id,
  cl.booking_id,
  cl.status,
  cl.error_message,
  cl.created_at,
  cl.updated_at,
  b.status AS booking_status
FROM checkout_leads cl
LEFT JOIN events e ON e.id = cl.event_id
LEFT JOIN ticket_categories tc ON tc.id = cl.category_id
LEFT JOIN bookings b ON b.id = cl.booking_id
WHERE (sqlc.arg(include_completed)::bool = true OR (cl.status NOT IN ('CONFIRMED') AND (b.status IS NULL OR b.status != 'CONFIRMED')))
  AND (sqlc.arg(event_id)::uuid = '00000000-0000-0000-0000-000000000000'::uuid OR cl.event_id = sqlc.arg(event_id))
  AND (
    sqlc.arg(query)::text = ''
    OR cl.contact_phone ILIKE '%' || sqlc.arg(query)::text || '%'
    OR COALESCE(cl.whatsapp_number, '') ILIKE '%' || sqlc.arg(query)::text || '%'
    OR COALESCE(cl.primary_name, '') ILIKE '%' || sqlc.arg(query)::text || '%'
    OR COALESCE(cl.upi_transaction_id, '') ILIKE '%' || sqlc.arg(query)::text || '%'
  )
ORDER BY cl.updated_at DESC
LIMIT sqlc.arg(page_limit)
OFFSET sqlc.arg(page_offset);
