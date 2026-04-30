#!/usr/bin/env bash
# One-time provisioning of an Ubuntu Server 24.04 host to run jetmon-bridge.
# Run this script once before the first deploy-prod.sh invocation.
# For subsequent updates, use deploy-prod.sh instead.
#
# What this script does:
#   - Builds the binary locally (requires Go 1.26+)
#   - Creates the jetmon-bridge system user on the server
#   - Creates /opt/jetmon-bridge with correct ownership and permissions
#   - Installs the binary, systemd service unit, and env file template
#   - Enables the service (but does NOT start it — configure the DSN first)
#
# The SSH user must have passwordless sudo on the remote host.
#
# Usage:
#   ./scripts/provision.sh <user@host>
#
# Example:
#   ./scripts/provision.sh deploy@jetmon-replica.prod.example.com

set -euo pipefail
cd "$(dirname "$0")/.."

REMOTE="${1:-}"
if [[ -z "${REMOTE}" ]]; then
  echo "Usage: $0 <user@host>" >&2
  exit 1
fi

DEPLOY_DIR="${DEPLOY_DIR:-/opt/jetmon-bridge}"
BINARY="bin/jetmon-bridge-linux-amd64"
STAGING="/tmp/jetmon-bridge-provision"

# ── 1. Build ─────────────────────────────────────────────────────────────────

if ! command -v go &>/dev/null; then
  echo "error: 'go' not found locally. Install Go 1.26+ to build the binary." >&2
  exit 1
fi

BUILD_VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
echo "==> Building jetmon-bridge ${BUILD_VERSION} for linux/amd64..."
mkdir -p bin
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w -X main.version=${BUILD_VERSION}" -o "${BINARY}" ./cmd/jetmon-bridge

# ── 2. Upload files ───────────────────────────────────────────────────────────

echo "==> Uploading files to ${REMOTE}..."
ssh "${REMOTE}" "mkdir -p ${STAGING}"
scp "${BINARY}"                      "${REMOTE}:${STAGING}/jetmon-bridge"
scp systemd/jetmon-bridge.service    "${REMOTE}:${STAGING}/jetmon-bridge.service"
scp systemd/env.sample               "${REMOTE}:${STAGING}/env.sample"

# ── 3. Server-side setup ──────────────────────────────────────────────────────

echo "==> Provisioning ${REMOTE}..."
# shellcheck disable=SC2029
ssh "${REMOTE}" "
  set -e

  # System user (idempotent)
  if id jetmon-bridge &>/dev/null; then
    echo '  [skip] user jetmon-bridge already exists'
  else
    sudo useradd --system --no-create-home --shell /usr/sbin/nologin jetmon-bridge
    echo '  [done] created system user jetmon-bridge'
  fi

  # Deploy directory
  sudo mkdir -p ${DEPLOY_DIR}
  sudo chown root:jetmon-bridge ${DEPLOY_DIR}
  sudo chmod 750 ${DEPLOY_DIR}
  echo '  [done] ${DEPLOY_DIR} ready'

  # Binary
  sudo mv ${STAGING}/jetmon-bridge ${DEPLOY_DIR}/jetmon-bridge
  sudo chown root:root              ${DEPLOY_DIR}/jetmon-bridge
  sudo chmod 755                    ${DEPLOY_DIR}/jetmon-bridge
  echo '  [done] binary installed'

  # Env file — only install from sample if not already present
  if [[ -f ${DEPLOY_DIR}/env ]]; then
    echo '  [skip] ${DEPLOY_DIR}/env already exists (existing config preserved)'
  else
    sudo cp    ${STAGING}/env.sample ${DEPLOY_DIR}/env
    sudo chown root:jetmon-bridge    ${DEPLOY_DIR}/env
    sudo chmod 640                   ${DEPLOY_DIR}/env
    echo '  [done] env file installed from template — edit it before starting'
  fi

  # systemd unit
  sudo mv    ${STAGING}/jetmon-bridge.service /etc/systemd/system/jetmon-bridge.service
  sudo chown root:root /etc/systemd/system/jetmon-bridge.service
  sudo chmod 644       /etc/systemd/system/jetmon-bridge.service
  sudo systemctl daemon-reload
  sudo systemctl enable jetmon-bridge
  echo '  [done] systemd unit installed and enabled'

  # Cleanup
  rm -rf ${STAGING}
"

# ── 4. Next steps ─────────────────────────────────────────────────────────────

echo ""
echo "==> Provisioning complete. The service is enabled but NOT yet started."
echo ""
echo "    Before starting, edit the environment file on the server and set JETMON_DSN:"
echo ""
echo "        ssh ${REMOTE} 'sudo nano ${DEPLOY_DIR}/env'"
echo ""
echo "    JETMON_DSN format:  user:password@tcp(replica-host:3306)/jetmon_db"
echo ""
echo "    Then start the service:"
echo ""
echo "        ssh ${REMOTE} 'sudo systemctl start jetmon-bridge'"
echo "        ssh ${REMOTE} 'sudo systemctl status jetmon-bridge'"
echo ""
echo "    For future updates, use:  ./scripts/deploy-prod.sh ${REMOTE}"
echo ""
