#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

: "${DATABASE_URL:?DATABASE_URL is required}"
ACTION="${1:-up}"

go run ./cmd/migrate "$ACTION"

if [[ "$ACTION" == "up" ]] && command -v sqlc >/dev/null 2>&1; then
	sqlc generate -f sqlc.yaml
fi
