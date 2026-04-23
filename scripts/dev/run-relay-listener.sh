#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BIN_DIR="$ROOT_DIR/.tmp/dev-bin"
TARGET_NS="${1:-ns-sink-a}"
LISTEN_ADDR="${2:-127.0.0.1:162}"

SUDO=""
if [[ "$(id -u)" -ne 0 ]]; then
  SUDO="sudo"
fi

exec ${SUDO} ip netns exec "$TARGET_NS" "$BIN_DIR/udp-listener" -listen "$LISTEN_ADDR"
