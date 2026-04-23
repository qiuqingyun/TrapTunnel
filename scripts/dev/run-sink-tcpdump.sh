#!/usr/bin/env bash
set -euo pipefail

INTERFACE="${1:-any}"

SUDO=""
if [[ "$(id -u)" -ne 0 ]]; then
  SUDO="sudo"
fi

exec ${SUDO} ip netns exec ns-sink tcpdump -n -i "${INTERFACE}" udp or tcp port 10000 or udp port 162
