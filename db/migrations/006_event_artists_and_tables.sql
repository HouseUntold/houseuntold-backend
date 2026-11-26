-- +goose Up

-- ============================================================
-- 1. Add Event Location Details & Formatted Description
-- ============================================================
ALTER TABLE events ADD COLUMN IF NOT EXISTS venue_name VARCHAR(255) NOT NULL DEFAULT 'Hotel Sky Blue';
ALTER TABLE events ADD COLUMN IF NOT EXISTS location_area VARCHAR(255) NOT NULL DEFAULT 'Ravet- Pune';
ALTER TABLE events ADD COLUMN IF NOT EXISTS transit_info VARCHAR(255) NOT NULL DEFAULT 'Near Ravet BRT Road';
ALTER TABLE events ADD COLUMN IF NOT EXISTS advisory_notice TEXT NOT NULL DEFAULT 'Valid government photo ID required for 21+ Alcoholic VIP & Couple zones.';
ALTER TABLE events ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '';

-- ============================================================
-- 2. Create Event Artists Table
-- ============================================================
CREATE TABLE IF NOT EXISTS event_artists (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  event_id UUID NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  name VARCHAR(255) NOT NULL,
  category VARCHAR(100) NOT NULL, -- e.g. 'Headliner DJ', 'Supporting Act', 'Live Vocalist'
  description TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_event_artists_event ON event_artists(event_id);

-- ============================================================
-- 3. Support Table Sizes 5, 8, 10, 15 in Seating & Categories
-- ============================================================
ALTER TABLE event_seating_inventory DROP CONSTRAINT IF EXISTS event_seating_inventory_table_size_check;
ALTER TABLE event_seating_inventory ADD CONSTRAINT event_seating_inventory_table_size_check CHECK (
  table_size IN (5, 8, 10, 15)
);

ALTER TABLE event_seat_reservations DROP CONSTRAINT IF EXISTS event_seat_reservations_table_size_check;
ALTER TABLE event_seat_reservations ADD CONSTRAINT event_seat_reservations_table_size_check CHECK (
  table_size IN (5, 8, 10, 15)
);

ALTER TABLE ticket_categories DROP CONSTRAINT IF EXISTS ticket_categories_entry_count_check;
ALTER TABLE ticket_categories ADD CONSTRAINT ticket_categories_entry_count_check CHECK (
  entry_count IN (1, 2, 5, 8, 10, 15)
);

-- ============================================================
-- 4. 5-Character Alphanumeric Barcode Format (e.g. A7X9P)
-- ============================================================
ALTER TABLE tickets DROP CONSTRAINT IF EXISTS chk_tickets_bar_code_format;
ALTER TABLE tickets ADD CONSTRAINT chk_tickets_bar_code_format CHECK (bar_code ~ '^[A-Z0-9]{5}$');

-- +goose Down
ALTER TABLE tickets DROP CONSTRAINT IF EXISTS chk_tickets_bar_code_format;
ALTER TABLE tickets ADD CONSTRAINT chk_tickets_bar_code_format CHECK (bar_code ~ '^[A-Z][0-9]{4}$');

ALTER TABLE ticket_categories DROP CONSTRAINT IF EXISTS ticket_categories_entry_count_check;
ALTER TABLE ticket_categories ADD CONSTRAINT ticket_categories_entry_count_check CHECK (entry_count IN (1, 2, 10, 15));

ALTER TABLE event_seat_reservations DROP CONSTRAINT IF EXISTS event_seat_reservations_table_size_check;
ALTER TABLE event_seat_reservations ADD CONSTRAINT event_seat_reservations_table_size_check CHECK (
  table_size IN (10, 15)
);

ALTER TABLE event_seating_inventory DROP CONSTRAINT IF EXISTS event_seating_inventory_table_size_check;
ALTER TABLE event_seating_inventory ADD CONSTRAINT event_seating_inventory_table_size_check CHECK (
  table_size IN (10, 15)
);

DROP TABLE IF EXISTS event_artists;
ALTER TABLE events DROP COLUMN IF EXISTS description;
ALTER TABLE events DROP COLUMN IF EXISTS advisory_notice;
ALTER TABLE events DROP COLUMN IF EXISTS transit_info;
ALTER TABLE events DROP COLUMN IF EXISTS location_area;
ALTER TABLE events DROP COLUMN IF EXISTS venue_name;
