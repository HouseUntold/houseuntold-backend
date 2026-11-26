#!/usr/bin/env bash
set -euo pipefail

NAME="${LOAD_DB_CONTAINER:-venu-loadtest-pg}"
PORT="${LOAD_DB_PORT:-55433}"

docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --name "$NAME" \
	-e POSTGRES_PASSWORD=venu \
	-e POSTGRES_USER=venu \
	-e POSTGRES_DB=venu \
	-p "$PORT:5432" \
	postgres:16-alpine \
	-c max_connections=500 >/dev/null

for _ in $(seq 1 60); do
	if docker exec "$NAME" pg_isready -U venu -d venu >/dev/null 2>&1; then
		break
	fi
	sleep 0.5
done
docker exec "$NAME" pg_isready -U venu -d venu
printf 'DATABASE_URL=postgres://venu:venu@127.0.0.1:%s/venu?sslmode=disable\n' "$PORT"
