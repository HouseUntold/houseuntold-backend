-- +goose Up
CREATE TABLE event_seating_inventory (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  event_id UUID NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  section VARCHAR(1) NOT NULL CHECK (section IN ('A', 'B', 'C')),
  table_size INT NOT NULL CHECK (table_size IN (10, 15)),
  total_tables INT NOT NULL CHECK (total_tables >= 0),
  ticket_category_id UUID REFERENCES ticket_categories(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (event_id, section, table_size)
);

CREATE TABLE event_seat_reservations (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  event_id UUID NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  section VARCHAR(1) NOT NULL CHECK (section IN ('A', 'B', 'C')),
  table_size INT NOT NULL CHECK (table_size IN (10, 15)),
  table_number INT NOT NULL CHECK (table_number > 0),
  booking_id UUID NOT NULL UNIQUE REFERENCES bookings(id) ON DELETE CASCADE,
  ticket_id UUID REFERENCES tickets(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (event_id, section, table_size, table_number)
);

CREATE UNIQUE INDEX idx_group_ticket_category_section_size ON ticket_categories (
  event_id,
  category,
  entry_count
) WHERE name LIKE 'GROUP_%';

ALTER TABLE tickets ADD COLUMN checked_in_by UUID REFERENCES staff_accounts(id);
ALTER TABLE bar_transactions ADD COLUMN delta_payment_method VARCHAR(10);
ALTER TABLE bar_transactions ADD COLUMN delta_collected_at TIMESTAMPTZ;

CREATE TABLE api_audit_logs (
  id BIGSERIAL PRIMARY KEY,
  request_id TEXT NOT NULL,
  method TEXT NOT NULL,
  path TEXT NOT NULL,
  route TEXT NOT NULL,
  status_code INT NOT NULL,
  duration_ms INT NOT NULL,
  actor_id UUID,
  actor_role TEXT,
  ip TEXT NOT NULL,
  user_agent TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_api_audit_logs_created_at ON api_audit_logs (created_at DESC);
CREATE INDEX idx_api_audit_logs_actor_id ON api_audit_logs (actor_id) WHERE actor_id IS NOT NULL;
CREATE INDEX idx_api_audit_logs_path ON api_audit_logs (path);

-- +goose Down
DROP INDEX IF EXISTS idx_api_audit_logs_path;
DROP INDEX IF EXISTS idx_api_audit_logs_actor_id;
DROP INDEX IF EXISTS idx_api_audit_logs_created_at;
DROP TABLE IF EXISTS api_audit_logs;
ALTER TABLE bar_transactions DROP COLUMN IF EXISTS delta_collected_at;
ALTER TABLE bar_transactions DROP COLUMN IF EXISTS delta_payment_method;
ALTER TABLE tickets DROP COLUMN IF EXISTS checked_in_by;
DROP INDEX IF EXISTS idx_group_ticket_category_section_size;
DROP TABLE IF EXISTS event_seat_reservations;
DROP TABLE IF EXISTS event_seating_inventory;
