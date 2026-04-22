#!/usr/bin/env bash
# Builds jetmon-bridge for linux/amd64 and deploys it to a provisioned host.
# The SSH user must have passwordless sudo on the remote host.
#
# Run scripts/provision.sh once before the first deployment to set up the
# system user, directory layout, and systemd service on the host.
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
