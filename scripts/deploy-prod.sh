#!/usr/bin/env bash
# Cross-compiles jetmon-bridge for linux/amd64 and deploys it to a remote host.
# The remote host must have systemd and the SSH user must have passwordless sudo.
#
# Prerequisites on the remote host (one-time setup):
#   sudo useradd -r -s /sbin/nologin jetmon-bridge
#   sudo mkdir -p /opt/jetmon-bridge
#   sudo cp systemd/env.sample /opt/jetmon-bridge/env
#   sudo chmod 640 /opt/jetmon-bridge/env
#   sudo chown root:jetmon-bridge /opt/jetmon-bridge/env
#   # Edit /opt/jetmon-bridge/env and set JETMON_DSN
#
# Usage:
#   ./scripts/deploy-prod.sh <user@host>
#
# Example:
#   ./scripts/deploy-prod.sh deploy@jetmon-replica.prod.example.com

set -euo pipefail
cd "$(dirname "$0")/.."

REMOTE="${1:-}"
if [[ -z "${REMOTE}" ]]; then
  echo "Usage: $0 <user@host>" >&2
  exit 1
fi

DEPLOY_DIR="${DEPLOY_DIR:-/opt/jetmon-bridge}"
BINARY="bin/jetmon-bridge-linux-amd64"

echo "Building jetmon-bridge for linux/amd64..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o "${BINARY}" .

echo "Uploading binary to ${REMOTE}:${DEPLOY_DIR}/..."
ssh "${REMOTE}" "mkdir -p ${DEPLOY_DIR}"
scp "${BINARY}" "${REMOTE}:${DEPLOY_DIR}/jetmon-bridge.new"

echo "Uploading systemd unit..."
scp systemd/jetmon-bridge.service "${REMOTE}:/tmp/jetmon-bridge.service"

echo "Installing on ${REMOTE}..."
# shellcheck disable=SC2029
ssh "${REMOTE}" "
  set -e
  sudo mv ${DEPLOY_DIR}/jetmon-bridge.new ${DEPLOY_DIR}/jetmon-bridge
  sudo chmod 755 ${DEPLOY_DIR}/jetmon-bridge
  sudo chown root:root ${DEPLOY_DIR}/jetmon-bridge
  sudo mv /tmp/jetmon-bridge.service /etc/systemd/system/jetmon-bridge.service
  sudo systemctl daemon-reload
  sudo systemctl enable jetmon-bridge
  sudo systemctl restart jetmon-bridge
  sudo systemctl --no-pager status jetmon-bridge
"

echo "Deployment to ${REMOTE} complete."
