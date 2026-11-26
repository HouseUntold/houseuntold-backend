-- +goose Up

-- ============================================================
-- 1. Fix age CHECK constraint on ticket_guests (18 → 21)
-- ============================================================
ALTER TABLE ticket_guests DROP CONSTRAINT IF EXISTS ticket_guests_age_check;
ALTER TABLE ticket_guests ADD CONSTRAINT ticket_guests_age_check CHECK (age >= 21 AND age <= 120);

-- ============================================================
-- 2. Add help_messages table
-- ============================================================
CREATE TABLE help_messages (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name VARCHAR(255) NOT NULL,
  phone VARCHAR(50) NOT NULL,
  message TEXT NOT NULL CHECK (char_length(message) <= 1000),
  event_id UUID REFERENCES events(id) ON DELETE SET NULL,
  is_resolved BOOLEAN NOT NULL DEFAULT false,
  resolved_by UUID REFERENCES staff_accounts(id) ON DELETE SET NULL,
  resolved_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_help_messages_resolved ON help_messages(is_resolved, created_at DESC);

-- ============================================================
-- 3. Missing indexes
-- ============================================================
CREATE INDEX IF NOT EXISTS idx_bookings_status_created ON bookings(status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_bar_transactions_ticket ON bar_transactions(ticket_id, created_at DESC);
-- New partial text-pattern index (distinct from idx_api_audit_logs_path added in 004)
CREATE INDEX IF NOT EXISTS idx_audit_logs_path ON api_audit_logs(path text_pattern_ops) WHERE path IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_ticket_guests_free_shot ON ticket_guests(
  free_shot_redeemed
) WHERE free_shot_redeemed = false;

-- ============================================================
-- 4. token_version on staff_accounts
--    Already added in migration 003 (INT NOT NULL DEFAULT 0).
--    Using IF NOT EXISTS guard for safety in case of partial rollback.
-- ============================================================
-- +goose StatementBegin
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM information_schema.columns
    WHERE table_name = 'staff_accounts' AND column_name = 'token_version'
  ) THEN
    ALTER TABLE staff_accounts ADD COLUMN token_version INTEGER NOT NULL DEFAULT 1;
  END IF;
END;
$$;
-- +goose StatementEnd

-- ============================================================
-- 5. payment_qr_data column on bookings
-- ============================================================
ALTER TABLE bookings ADD COLUMN IF NOT EXISTS payment_qr_data TEXT;

-- +goose Down

ALTER TABLE bookings DROP COLUMN IF EXISTS payment_qr_data;

-- token_version: only drop if we added it in this migration (not from 003)
-- Left intentionally as no-op; 003 Down handles its own column.

DROP INDEX IF EXISTS idx_ticket_guests_free_shot;
DROP INDEX IF EXISTS idx_audit_logs_path;
DROP INDEX IF EXISTS idx_bar_transactions_ticket;
DROP INDEX IF EXISTS idx_bookings_status_created;

DROP INDEX IF EXISTS idx_help_messages_resolved;
DROP TABLE IF EXISTS help_messages;

-- Restore age CHECK to >= 18
ALTER TABLE ticket_guests DROP CONSTRAINT IF EXISTS ticket_guests_age_check;
ALTER TABLE ticket_guests ADD CONSTRAINT ticket_guests_age_check CHECK (age >= 18 AND age <= 120);
