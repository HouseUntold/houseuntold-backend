#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

: "${DATABASE_URL:?DATABASE_URL is required}"

export PORT="${PORT:-18084}"
export APP_ENV="${APP_ENV:-development}"
export APP_URL="http://127.0.0.1:${PORT}"
export ALLOWED_ORIGINS="${ALLOWED_ORIGINS:-$APP_URL}"
export TRUSTED_PROXIES="${TRUSTED_PROXIES:-127.0.0.1}"
export JWT_ACCESS_SECRET="${JWT_ACCESS_SECRET:-soak-access-secret-change-me}"
export JWT_REFRESH_SECRET="${JWT_REFRESH_SECRET:-soak-refresh-secret-change-me}"
export JWT_AUDIENCE="${JWT_AUDIENCE:-venu}"
export JWT_ISSUER="${JWT_ISSUER:-venu-api}"
export QR_SIGNING_SECRET="${QR_SIGNING_SECRET:-soak-qr-secret-change-me-32-bytes}"
export UPI_VPA="${UPI_VPA:-venu.soak@upi}"
export UPI_MERCHANT_NAME="${UPI_MERCHANT_NAME:-House Untold Soak Test}"
export ACCESS_TOKEN_TTL="${ACCESS_TOKEN_TTL:-30m}"
export RUN_MIGRATIONS_ON_START=false
export DB_MAX_CONNS="${DB_MAX_CONNS:-20}"
export DB_MIN_CONNS="${DB_MIN_CONNS:-5}"
export DB_QUERY_TIMEOUT="${DB_QUERY_TIMEOUT:-10s}"
export HTTP_REQUEST_TIMEOUT="${HTTP_REQUEST_TIMEOUT:-30s}"
export RATE_LIMIT_AUTH_MAX="${RATE_LIMIT_AUTH_MAX:-100}"
export RATE_LIMIT_CHECKOUT_MAX="${RATE_LIMIT_CHECKOUT_MAX:-2000}"
export RATE_LIMIT_SCAN_MAX="${RATE_LIMIT_SCAN_MAX:-2000}"
export RATE_LIMIT_BAR_MAX="${RATE_LIMIT_BAR_MAX:-2000}"

MANIFEST_PATH="${VENU_SEED_MANIFEST:-.loadtest/seed.json}"
if [[ "$MANIFEST_PATH" != /* ]]; then
	MANIFEST_PATH="$(pwd)/$MANIFEST_PATH"
fi
export VENU_SEED_MANIFEST="$MANIFEST_PATH"
export SEED_MANIFEST="$MANIFEST_PATH"

go run ./cmd/migrate reset
go run ./cmd/migrate up
go run ./cmd/seed

go build -o /tmp/venu-api-soak ./cmd/api
/tmp/venu-api-soak &
SERVER_PID=$!
trap 'kill "$SERVER_PID" 2>/dev/null || true' EXIT

for _ in $(seq 1 60); do
	if curl -fsS "$APP_URL/api/health" >/dev/null 2>&1; then
		break
	fi
	sleep 0.5
done
curl -fsS "$APP_URL/api/health" >/dev/null

go run ./cmd/loadtest \
	-base-url "$APP_URL" \
	-seed-file "$MANIFEST_PATH" \
	-duration "${SOAK_DURATION:-10m}" \
	-gate-workers "${SOAK_GATE_WORKERS:-5}" \
	-gate-total "${SOAK_GATE_TOTAL:-2000}" \
	-bar-workers "${SOAK_BAR_WORKERS:-5}" \
	-bar-total "${SOAK_BAR_TOTAL:-1000}" \
	-checkout-users "${SOAK_CHECKOUT_USERS:-100}" \
	-checkout-total "${SOAK_CHECKOUT_TOTAL:-1000}" \
	-cleanup
