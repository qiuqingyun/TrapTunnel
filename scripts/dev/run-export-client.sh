#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BIN_DIR="$ROOT_DIR/.tmp/dev-bin"
TARGET_NS="${1:-ns-edge}"
ADDR="${2:-10.10.2.2:12000}"
COUNT="${3:-1}"

SUDO=""
if [[ "$(id -u)" -ne 0 ]]; then
  SUDO="sudo"
fi

exec ${SUDO} ip netns exec "$TARGET_NS" "$BIN_DIR/export-client" -addr "$ADDR" -count "$COUNT"
