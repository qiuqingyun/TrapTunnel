#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BIN_DIR="$ROOT_DIR/.tmp/dev-bin"
CONFIG="${1:-$ROOT_DIR/examples/relay-test-edge.toml}"

SUDO=""
if [[ "$(id -u)" -ne 0 ]]; then
  SUDO="sudo"
fi

exec ${SUDO} ip netns exec ns-edge "$BIN_DIR/node" -c "$CONFIG"
