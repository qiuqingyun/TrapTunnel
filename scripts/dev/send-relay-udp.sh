#!/usr/bin/env bash
set -euo pipefail

DEST_IP="${1:-10.20.1.1}"
DEST_PORT="${2:-162}"
PAYLOAD="${3:-relay-trap-test}"

SUDO=""
if [[ "$(id -u)" -ne 0 ]]; then
  SUDO="sudo"
fi

printf "%s" "$PAYLOAD" | ${SUDO} ip netns exec ns-device socat -u - "UDP:${DEST_IP}:${DEST_PORT}"
