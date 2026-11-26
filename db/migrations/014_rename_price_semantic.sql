-- +goose Up
-- ticket_categories.base_price is the canonical amount column (per-person for tiers, total for table/group categories).
-- Seating API previously exposed it as price_per_person; code now exposes table_total. No column rename needed.
SELECT 1;

-- +goose Down
SELECT 1;
