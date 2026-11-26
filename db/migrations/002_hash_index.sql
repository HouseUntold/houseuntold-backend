-- +goose Up
DROP INDEX IF EXISTS idx_tickets_qr_digest;
CREATE INDEX idx_tickets_qr_digest_hash ON tickets USING HASH (qr_token_digest);

-- +goose Down
DROP INDEX IF EXISTS idx_tickets_qr_digest_hash;
CREATE INDEX idx_tickets_qr_digest ON tickets (qr_token_digest);
