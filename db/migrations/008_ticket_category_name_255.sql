-- +goose Up
ALTER TABLE ticket_categories ALTER COLUMN name TYPE VARCHAR(255);

-- +goose Down
ALTER TABLE ticket_categories ALTER COLUMN name TYPE VARCHAR(100);
