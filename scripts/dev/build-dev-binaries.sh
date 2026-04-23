#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BIN_DIR="$ROOT_DIR/.tmp/dev-bin"

mkdir -p "$BIN_DIR"

GO_BIN="${GO_BIN:-go}"
if ! command -v "$GO_BIN" >/dev/null 2>&1; then
  if [[ -x "$HOME/.local/go/bin/go" ]]; then
    GO_BIN="$HOME/.local/go/bin/go"
  else
    printf "[build-dev-binaries] go not found; set GO_BIN or install Go\n" >&2
    exit 1
  fi
fi

printf "[build-dev-binaries] using %s\n" "$GO_BIN"
"$GO_BIN" build -o "$BIN_DIR/node" "$ROOT_DIR/cmd/node"
"$GO_BIN" build -o "$BIN_DIR/udp-listener" "$ROOT_DIR/cmd/udp-listener"

printf "[build-dev-binaries] built:\n"
printf "  %s\n" "$BIN_DIR/node"
printf "  %s\n" "$BIN_DIR/udp-listener"
