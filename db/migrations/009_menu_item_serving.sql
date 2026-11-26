-- +goose Up
ALTER TABLE menu_items ADD COLUMN IF NOT EXISTS serving_label VARCHAR(32) NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE menu_items DROP COLUMN IF EXISTS serving_label;
