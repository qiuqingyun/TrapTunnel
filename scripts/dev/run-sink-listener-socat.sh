#!/usr/bin/env bash
set -euo pipefail

LISTEN_ADDR="${1:-127.0.0.1}"
LISTEN_PORT="${2:-162}"

SUDO=""
if [[ "$(id -u)" -ne 0 ]]; then
  SUDO="sudo"
fi

exec ${SUDO} ip netns exec ns-sink socat -d -d -v "UDP4-RECVFROM:${LISTEN_PORT},bind=${LISTEN_ADDR},fork" STDOUT
