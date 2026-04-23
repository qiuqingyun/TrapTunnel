#!/usr/bin/env bash
set -euo pipefail

SUDO=""
if [[ "$(id -u)" -ne 0 ]]; then
  SUDO="sudo"
fi

for ns in ns-device ns-edge ns-relay ns-sink-a ns-sink-b; do
  if ${SUDO} ip netns list | awk '{print $1}' | grep -Fx "$ns" >/dev/null 2>&1; then
    printf "[cleanup-relay-netns] delete %s\n" "$ns"
    ${SUDO} ip netns delete "$ns"
  fi
done
