#!/usr/bin/env bash
set -euo pipefail

TARGET_IP="${1:-10.10.1.1}"
TARGET_PORT="${2:-162}"
PAYLOAD="${3:-trap-test}"

SUDO=""
if [[ "$(id -u)" -ne 0 ]]; then
  SUDO="sudo"
fi

${SUDO} ip netns exec ns-device bash -lc "printf '%s' '$PAYLOAD' >/dev/udp/$TARGET_IP/$TARGET_PORT"
printf "[send-udp] sent payload=%q to %s:%s\n" "$PAYLOAD" "$TARGET_IP" "$TARGET_PORT"
