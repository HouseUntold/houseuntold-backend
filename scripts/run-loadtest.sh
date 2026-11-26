#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

: "${DATABASE_URL:?DATABASE_URL is required}"

export PORT="${PORT:-18080}"
export APP_URL="http://127.0.0.1:${PORT}"
export ALLOWED_ORIGINS="${ALLOWED_ORIGINS:-$APP_URL}"
export TRUSTED_PROXIES="${TRUSTED_PROXIES:-127.0.0.1}"
export JWT_ACCESS_SECRET="${JWT_ACCESS_SECRET:-loadtest-access-secret-change-me}"
export JWT_REFRESH_SECRET="${JWT_REFRESH_SECRET:-loadtest-refresh-secret-change-me}"
export JWT_AUDIENCE="${JWT_AUDIENCE:-venu}"
export JWT_ISSUER="${JWT_ISSUER:-venu-api}"
export QR_SIGNING_SECRET="${QR_SIGNING_SECRET:-loadtest-qr-secret-change-me-32-bytes}"
export UPI_VPA="${UPI_VPA:-venu.loadtest@upi}"
export UPI_MERCHANT_NAME="${UPI_MERCHANT_NAME:-House Untold Load Test}"
export DB_MAX_CONNS="${DB_MAX_CONNS:-100}"
export DB_MIN_CONNS="${DB_MIN_CONNS:-10}"
export DB_QUERY_TIMEOUT="${DB_QUERY_TIMEOUT:-10s}"
export HTTP_REQUEST_TIMEOUT="${HTTP_REQUEST_TIMEOUT:-30s}"
export RATE_LIMIT_AUTH_MAX="${RATE_LIMIT_AUTH_MAX:-20}"
export RATE_LIMIT_CHECKOUT_MAX="${RATE_LIMIT_CHECKOUT_MAX:-2000}"
export RATE_LIMIT_SCAN_MAX="${RATE_LIMIT_SCAN_MAX:-2000}"
export RATE_LIMIT_BAR_MAX="${RATE_LIMIT_BAR_MAX:-2000}"

if [[ "${SKIP_MIGRATE:-0}" != "1" ]]; then
	go run ./cmd/migrate reset
	go run ./cmd/migrate up
fi

MANIFEST_PATH="${VENU_SEED_MANIFEST:-.loadtest/seed.json}"
if [[ "$MANIFEST_PATH" != /* ]]; then
	MANIFEST_PATH="$(pwd)/$MANIFEST_PATH"
fi
export VENU_SEED_MANIFEST="$MANIFEST_PATH"

go build -o /tmp/venu-api-loadtest ./cmd/api
go run ./cmd/seed

/tmp/venu-api-loadtest &
SERVER_PID=$!
trap 'kill "$SERVER_PID" 2>/dev/null || true' EXIT

for _ in $(seq 1 60); do
	if curl -fsS "$APP_URL/api/health" >/dev/null 2>&1; then
		break
	fi
	sleep 0.5
done
curl -fsS "$APP_URL/api/health" >/dev/null

export VENU_LOADTEST=1
export VENU_BASE_URL="$APP_URL"
export VENU_TEST_DATABASE_URL="$DATABASE_URL"

go test -v -count=1 -timeout 30m ./loadtest
