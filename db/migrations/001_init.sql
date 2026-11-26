-- +goose Up
-- House Untold Ticketing Platform — Production Schema (GA Ticketing, Manual UPI Verification, Dual Barcodes)

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- ==========================================
-- 1. ENUMS (Strict Type Safety)
-- ==========================================
CREATE TYPE staff_role AS ENUM ('ADMIN', 'SCANNER', 'BARTENDER');
CREATE TYPE event_status AS ENUM ('DRAFT', 'ON_SALE', 'SOLD_OUT', 'ENDED');
CREATE TYPE booking_status AS ENUM ('PENDING', 'CONFIRMED', 'FAILED');
CREATE TYPE gender_type AS ENUM ('MALE', 'FEMALE', 'OTHER');
CREATE TYPE menu_category AS ENUM ('DRINK', 'FOOD');

-- ==========================================
-- 2. TABLES
-- ==========================================

CREATE TABLE staff_accounts (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  username VARCHAR(255) UNIQUE NOT NULL,
  password_hash VARCHAR(255) NOT NULL,
  role staff_role NOT NULL,
  is_active BOOLEAN NOT NULL DEFAULT true,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE rate_limits (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  identifier VARCHAR(255) NOT NULL,
  endpoint VARCHAR(255) NOT NULL,
  hits INT NOT NULL DEFAULT 1,
  window_start TIMESTAMPTZ NOT NULL
);

CREATE TABLE events (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  title VARCHAR(255) NOT NULL,
  starts_at TIMESTAMPTZ NOT NULL,
  ends_at TIMESTAMPTZ NOT NULL,
  location VARCHAR(255) NOT NULL,
  status event_status NOT NULL DEFAULT 'DRAFT',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CONSTRAINT chk_events_time_window CHECK (ends_at > starts_at)
);

CREATE TABLE ticket_categories (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  event_id UUID NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  name VARCHAR(100) NOT NULL, -- STAG_MALE, STAG_FEMALE, COUPLE, TABLE
  category VARCHAR(50), -- A, B, C (for tables)
  base_price INT NOT NULL CHECK (base_price >= 0),
  fnb_credit INT NOT NULL CHECK (fnb_credit >= 0),
  entry_count INT NOT NULL CHECK (entry_count IN (1, 2, 10, 15)),
  is_gender_strict BOOLEAN NOT NULL DEFAULT false
);

CREATE TABLE bookings (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  event_id UUID NOT NULL REFERENCES events(id),
  category_id UUID NOT NULL REFERENCES ticket_categories(id),
  contact_phone VARCHAR(50) NOT NULL,
  contact_email VARCHAR(255) NOT NULL,
  total_amount INT NOT NULL CHECK (total_amount >= 0),
  upi_transaction_id VARCHAR(100), -- User-submitted UPI UTR / transaction reference
  status booking_status NOT NULL DEFAULT 'PENDING',
  verified_by UUID REFERENCES staff_accounts(id),
  verified_at TIMESTAMPTZ,
  rejection_reason VARCHAR(255),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE tickets (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  booking_id UUID NOT NULL UNIQUE REFERENCES bookings(id) ON DELETE CASCADE,
  event_id UUID NOT NULL REFERENCES events(id),
  qr_token_digest VARCHAR(255) UNIQUE NOT NULL, -- Gate QR: HMAC-SHA256 digest (single-use, expires on scan)
  bar_code VARCHAR(5) UNIQUE NOT NULL, -- Bartender 1D linear barcode: 1 uppercase letter + 4 digits (e.g. A1234)
  food_balance INT NOT NULL CHECK (food_balance >= 0),
  initial_food_balance INT NOT NULL CHECK (initial_food_balance >= 0),
  is_checked_in BOOLEAN NOT NULL DEFAULT false,
  checked_in_at TIMESTAMPTZ,
  CONSTRAINT chk_tickets_bar_code_format CHECK (bar_code ~ '^[A-Z][0-9]{4}$')
);

CREATE TABLE ticket_guests (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  ticket_id UUID NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  name VARCHAR(255) NOT NULL,
  age INT NOT NULL CHECK (age >= 18 AND age <= 120),
  gender gender_type NOT NULL,
  is_primary_contact BOOLEAN NOT NULL DEFAULT false,
  free_shot_redeemed BOOLEAN NOT NULL DEFAULT false
);

CREATE TABLE menu_items (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name VARCHAR(255) NOT NULL,
  category menu_category NOT NULL,
  unit_price INT NOT NULL CHECK (unit_price > 0),
  is_active BOOLEAN NOT NULL DEFAULT true
);

CREATE TABLE bar_transactions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  ticket_id UUID NOT NULL REFERENCES tickets(id),
  bartender_id UUID NOT NULL REFERENCES staff_accounts(id),
  idempotency_key VARCHAR(100),
  total_bill INT NOT NULL CHECK (total_bill >= 0),
  wallet_deducted INT NOT NULL CHECK (wallet_deducted >= 0),
  upi_delta_paid INT NOT NULL CHECK (upi_delta_paid >= 0),
  balance_before INT NOT NULL CHECK (balance_before >= 0),
  balance_after INT NOT NULL CHECK (balance_after >= 0),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE transaction_line_items (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  transaction_id UUID NOT NULL REFERENCES bar_transactions(id) ON DELETE CASCADE,
  menu_item_id UUID NOT NULL REFERENCES menu_items(id),
  quantity INT NOT NULL CHECK (quantity > 0),
  unit_price_at_sale INT NOT NULL CHECK (unit_price_at_sale > 0)
);

-- ==========================================
-- 3. INDEXES (High-Concurrency Performance)
-- ==========================================
-- Rate Limiting
CREATE UNIQUE INDEX idx_rate_limits_lookup ON rate_limits (identifier, endpoint, window_start);
CREATE INDEX idx_rate_limits_window_start ON rate_limits (window_start);

-- Bookings & Admin Verification Queue
CREATE INDEX idx_ticket_categories_event_id ON ticket_categories (event_id);
CREATE INDEX idx_bookings_event_status ON bookings (event_id, status);
CREATE INDEX idx_bookings_status_created ON bookings (status, created_at DESC);
CREATE UNIQUE INDEX idx_bookings_upi_txn_id ON bookings (upi_transaction_id) WHERE upi_transaction_id IS NOT NULL;

-- Tickets, Gate & Bartender POS (Hot Paths)
CREATE INDEX idx_tickets_booking_id ON tickets (booking_id);
CREATE INDEX idx_tickets_event_id ON tickets (event_id);
CREATE INDEX idx_ticket_guests_ticket_id ON ticket_guests (ticket_id);
CREATE INDEX idx_tickets_bar_code ON tickets (bar_code);
CREATE INDEX idx_tickets_qr_digest ON tickets (qr_token_digest);

-- Bar Transactions & Audit Traceability
CREATE INDEX idx_bar_transactions_ticket_created ON bar_transactions (ticket_id, created_at DESC);
CREATE UNIQUE INDEX idx_bar_transactions_idempotency ON bar_transactions (
  idempotency_key
) WHERE idempotency_key IS NOT NULL;
CREATE INDEX idx_transaction_line_items_tx_id ON transaction_line_items (transaction_id);

-- Active Menu Listing
CREATE INDEX idx_menu_items_active_cat ON menu_items (is_active, category, name);

-- ==========================================
-- 4. COMMENTS (Documentation for SQLC)
-- ==========================================
COMMENT ON COLUMN events.ends_at IS 'Used to automatically expire F&B wallets after event end';
COMMENT ON COLUMN bookings.upi_transaction_id IS 'Unique UPI UTR/transaction reference submitted by patron for manual admin verification';
COMMENT ON COLUMN tickets.qr_token_digest IS 'Gate QR HMAC-SHA256 digest — single-use, expires immediately on gate check-in';
COMMENT ON COLUMN tickets.bar_code IS 'Bartender 1D linear barcode — 1 uppercase letter + 4 digits (e.g. A1234), multi-use until event end';
COMMENT ON COLUMN bar_transactions.upi_delta_paid IS 'Excess amount collected at bar counter by bartender; stored for audit reference';

-- +goose Down
DROP TABLE IF EXISTS transaction_line_items;
DROP TABLE IF EXISTS bar_transactions;
DROP TABLE IF EXISTS menu_items;
DROP TABLE IF EXISTS ticket_guests;
DROP TABLE IF EXISTS tickets;
DROP TABLE IF EXISTS bookings;
DROP TABLE IF EXISTS ticket_categories;
DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS rate_limits;
DROP TABLE IF EXISTS staff_accounts;
DROP TYPE IF EXISTS menu_category;
DROP TYPE IF EXISTS gender_type;
DROP TYPE IF EXISTS booking_status;
DROP TYPE IF EXISTS event_status;
DROP TYPE IF EXISTS staff_role;
