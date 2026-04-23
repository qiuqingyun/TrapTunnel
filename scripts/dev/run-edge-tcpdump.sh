#!/usr/bin/env bash
set -euo pipefail

INTERFACE="${1:-any}"

SUDO=""
if [[ "$(id -u)" -ne 0 ]]; then
  SUDO="sudo"
fi

exec ${SUDO} ip netns exec ns-edge tcpdump -n -i "${INTERFACE}" udp or tcp port 10000
