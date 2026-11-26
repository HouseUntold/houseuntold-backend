-- +goose Up
CREATE TABLE IF NOT EXISTS checkout_leads (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  idempotency_key VARCHAR(255) NOT NULL,
  event_id UUID REFERENCES events(id),
  category_id UUID REFERENCES ticket_categories(id),
  contact_phone VARCHAR(50) NOT NULL,
  whatsapp_number VARCHAR(50),
  contact_email VARCHAR(255),
  primary_name VARCHAR(255),
  primary_age INT,
  gender VARCHAR(20),
  selection_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  upi_transaction_id VARCHAR(100),
  booking_id UUID REFERENCES bookings(id),
  status VARCHAR(30) NOT NULL DEFAULT 'ATTEMPTED',
  error_message TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_checkout_leads_idempotency_key ON checkout_leads(idempotency_key);
CREATE INDEX IF NOT EXISTS idx_checkout_leads_created_at ON checkout_leads(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_checkout_leads_contact_phone ON checkout_leads(contact_phone);
CREATE INDEX IF NOT EXISTS idx_checkout_leads_booking_id ON checkout_leads(booking_id);

-- +goose Down
DROP TABLE IF EXISTS checkout_leads;
