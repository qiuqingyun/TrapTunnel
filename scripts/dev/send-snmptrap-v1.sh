#!/usr/bin/env bash
set -euo pipefail

TARGET_IP="${1:-10.10.1.1}"
TARGET_PORT="${2:-162}"
COMMUNITY="${3:-public}"
AGENT_IP="${4:-10.10.1.2}"

SUDO=""
if [[ "$(id -u)" -ne 0 ]]; then
  SUDO="sudo"
fi

${SUDO} ip netns exec ns-device snmptrap \
  -v 1 \
  -c "${COMMUNITY}" \
  "${TARGET_IP}:${TARGET_PORT}" \
  1.3.6.1.4.1.8072.2.3.0.1 \
  "${AGENT_IP}" \
  6 \
  1 \
  12345 \
  1.3.6.1.2.1.1.1.0 s "trap-test"

printf "[send-snmptrap-v1] sent to %s:%s community=%s agent=%s\n" "$TARGET_IP" "$TARGET_PORT" "$COMMUNITY" "$AGENT_IP"
