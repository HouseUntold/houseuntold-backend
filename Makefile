.PHONY: migrate-up migrate-down seed-admin dev

migrate-up:
	go run ./cmd/migrate up

migrate-down:
	go run ./cmd/migrate down

seed-admin:
	psql "$$DATABASE_URL" -f db/seeds/real_world.sql

dev:
	go run ./cmd/api
