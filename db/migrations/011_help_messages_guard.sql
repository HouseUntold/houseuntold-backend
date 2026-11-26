-- +goose Up

CREATE TABLE IF NOT EXISTS help_messages (
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

CREATE INDEX IF NOT EXISTS idx_help_messages_resolved ON help_messages(is_resolved, created_at DESC);

-- +goose Down

SELECT 1;
