#!/usr/bin/env bash
set -euo pipefail

NS_DEVICE="ns-device"
NS_EDGE="ns-edge"
NS_SINK="ns-sink"

VETH_DEVICE="veth-device"
VETH_EDGE_IN="veth-edge-in"
VETH_EDGE_OUT="veth-edge-out"
VETH_SINK="veth-sink"

DEVICE_IP="10.10.1.2/24"
EDGE_IN_IP="10.10.1.1/24"
EDGE_OUT_IP="10.10.2.1/24"
SINK_IP="10.10.2.2/24"

SUDO=""
if [[ "$(id -u)" -ne 0 ]]; then
  SUDO="sudo"
fi

log() {
  printf "[setup-netns] %s\n" "$*"
}

ns_exists() {
  ${SUDO} ip netns list | awk '{print $1}' | grep -Fx "$1" >/dev/null 2>&1
}

cleanup_if_exists() {
  if ns_exists "$1"; then
    log "delete existing namespace $1"
    ${SUDO} ip netns delete "$1"
  fi
}

cleanup_if_exists "$NS_DEVICE"
cleanup_if_exists "$NS_EDGE"
cleanup_if_exists "$NS_SINK"

log "create namespaces"
${SUDO} ip netns add "$NS_DEVICE"
${SUDO} ip netns add "$NS_EDGE"
${SUDO} ip netns add "$NS_SINK"

log "create veth pairs"
${SUDO} ip link add "$VETH_DEVICE" type veth peer name "$VETH_EDGE_IN"
${SUDO} ip link add "$VETH_EDGE_OUT" type veth peer name "$VETH_SINK"

log "move links into namespaces"
${SUDO} ip link set "$VETH_DEVICE" netns "$NS_DEVICE"
${SUDO} ip link set "$VETH_EDGE_IN" netns "$NS_EDGE"
${SUDO} ip link set "$VETH_EDGE_OUT" netns "$NS_EDGE"
${SUDO} ip link set "$VETH_SINK" netns "$NS_SINK"

log "assign addresses"
${SUDO} ip -n "$NS_DEVICE" addr add "$DEVICE_IP" dev "$VETH_DEVICE"
${SUDO} ip -n "$NS_EDGE" addr add "$EDGE_IN_IP" dev "$VETH_EDGE_IN"
${SUDO} ip -n "$NS_EDGE" addr add "$EDGE_OUT_IP" dev "$VETH_EDGE_OUT"
${SUDO} ip -n "$NS_SINK" addr add "$SINK_IP" dev "$VETH_SINK"

log "bring loopback up"
${SUDO} ip -n "$NS_DEVICE" link set lo up
${SUDO} ip -n "$NS_EDGE" link set lo up
${SUDO} ip -n "$NS_SINK" link set lo up

log "bring interfaces up"
${SUDO} ip -n "$NS_DEVICE" link set "$VETH_DEVICE" up
${SUDO} ip -n "$NS_EDGE" link set "$VETH_EDGE_IN" up
${SUDO} ip -n "$NS_EDGE" link set "$VETH_EDGE_OUT" up
${SUDO} ip -n "$NS_SINK" link set "$VETH_SINK" up

log "configure routes"
${SUDO} ip -n "$NS_DEVICE" route add 10.10.2.0/24 via 10.10.1.1
${SUDO} ip -n "$NS_SINK" route add 10.10.1.0/24 via 10.10.2.1

log "done"
${SUDO} ip netns list
