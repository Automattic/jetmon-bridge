#!/usr/bin/env bash
# Builds and runs jetmon-bridge locally against a direct MySQL DSN.
# For Docker-based local testing, use docker-compose instead.
#
# Usage:
#   JETMON_DSN="user:pass@tcp(localhost:3306)/jetmon_db" ./scripts/deploy-local.sh
#
# Optional env vars:
#   JETMON_DSN          MySQL DSN (default: root:@tcp(localhost:3306)/jetmon_db)
#   JETMON_ADDR         Listen address (default: 127.0.0.1:7400)
#   JETMON_READ_TIMEOUT DB query timeout (default: 5s)

set -euo pipefail
cd "$(dirname "$0")/.."

DSN="${JETMON_DSN:-root:@tcp(localhost:3306)/jetmon_db}"
ADDR="${JETMON_ADDR:-127.0.0.1:7400}"
READ_TIMEOUT="${JETMON_READ_TIMEOUT:-5s}"

echo "Building jetmon-bridge..."
go build -o bin/jetmon-bridge .

echo "Starting jetmon-bridge on ${ADDR}..."
exec bin/jetmon-bridge \
  -dsn       "${DSN}" \
  -addr      "${ADDR}" \
  -read-timeout "${READ_TIMEOUT}"
