#!/usr/bin/env bash
set -euo pipefail

NS_DEVICE="ns-device"
NS_EDGE="ns-edge"
NS_RELAY="ns-relay"
NS_SINK_A="ns-sink-a"
NS_SINK_B="ns-sink-b"

VETH_DEVICE="vdev0"
VETH_EDGE_IN="vedge0"
VETH_EDGE_OUT="vedge1"
VETH_RELAY_IN="vrel0"
VETH_RELAY_OUT_A="vrel1"
VETH_SINK_A="vsink0"
VETH_RELAY_OUT_B="vrel2"
VETH_SINK_B="vsink1"

DEVICE_IP="10.20.1.2/24"
EDGE_IN_IP="10.20.1.1/24"
EDGE_OUT_IP="10.20.2.1/24"
RELAY_IN_IP="10.20.2.2/24"
RELAY_OUT_A_IP="10.20.3.1/24"
SINK_A_IP="10.20.3.2/24"
RELAY_OUT_B_IP="10.20.4.1/24"
SINK_B_IP="10.20.4.2/24"

SUDO=""
if [[ "$(id -u)" -ne 0 ]]; then
  SUDO="sudo"
fi

log() {
  printf "[setup-relay-netns] %s\n" "$*"
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

for ns in "$NS_DEVICE" "$NS_EDGE" "$NS_RELAY" "$NS_SINK_A" "$NS_SINK_B"; do
  cleanup_if_exists "$ns"
done

log "create namespaces"
${SUDO} ip netns add "$NS_DEVICE"
${SUDO} ip netns add "$NS_EDGE"
${SUDO} ip netns add "$NS_RELAY"
${SUDO} ip netns add "$NS_SINK_A"
${SUDO} ip netns add "$NS_SINK_B"

log "create veth pairs"
${SUDO} ip link add "$VETH_DEVICE" type veth peer name "$VETH_EDGE_IN"
${SUDO} ip link add "$VETH_EDGE_OUT" type veth peer name "$VETH_RELAY_IN"
${SUDO} ip link add "$VETH_RELAY_OUT_A" type veth peer name "$VETH_SINK_A"
${SUDO} ip link add "$VETH_RELAY_OUT_B" type veth peer name "$VETH_SINK_B"

log "move links into namespaces"
${SUDO} ip link set "$VETH_DEVICE" netns "$NS_DEVICE"
${SUDO} ip link set "$VETH_EDGE_IN" netns "$NS_EDGE"
${SUDO} ip link set "$VETH_EDGE_OUT" netns "$NS_EDGE"
${SUDO} ip link set "$VETH_RELAY_IN" netns "$NS_RELAY"
${SUDO} ip link set "$VETH_RELAY_OUT_A" netns "$NS_RELAY"
${SUDO} ip link set "$VETH_RELAY_OUT_B" netns "$NS_RELAY"
${SUDO} ip link set "$VETH_SINK_A" netns "$NS_SINK_A"
${SUDO} ip link set "$VETH_SINK_B" netns "$NS_SINK_B"

log "assign addresses"
${SUDO} ip -n "$NS_DEVICE" addr add "$DEVICE_IP" dev "$VETH_DEVICE"
${SUDO} ip -n "$NS_EDGE" addr add "$EDGE_IN_IP" dev "$VETH_EDGE_IN"
${SUDO} ip -n "$NS_EDGE" addr add "$EDGE_OUT_IP" dev "$VETH_EDGE_OUT"
${SUDO} ip -n "$NS_RELAY" addr add "$RELAY_IN_IP" dev "$VETH_RELAY_IN"
${SUDO} ip -n "$NS_RELAY" addr add "$RELAY_OUT_A_IP" dev "$VETH_RELAY_OUT_A"
${SUDO} ip -n "$NS_RELAY" addr add "$RELAY_OUT_B_IP" dev "$VETH_RELAY_OUT_B"
${SUDO} ip -n "$NS_SINK_A" addr add "$SINK_A_IP" dev "$VETH_SINK_A"
${SUDO} ip -n "$NS_SINK_B" addr add "$SINK_B_IP" dev "$VETH_SINK_B"

log "bring loopback up"
for ns in "$NS_DEVICE" "$NS_EDGE" "$NS_RELAY" "$NS_SINK_A" "$NS_SINK_B"; do
  ${SUDO} ip -n "$ns" link set lo up
done

log "bring interfaces up"
${SUDO} ip -n "$NS_DEVICE" link set "$VETH_DEVICE" up
${SUDO} ip -n "$NS_EDGE" link set "$VETH_EDGE_IN" up
${SUDO} ip -n "$NS_EDGE" link set "$VETH_EDGE_OUT" up
${SUDO} ip -n "$NS_RELAY" link set "$VETH_RELAY_IN" up
${SUDO} ip -n "$NS_RELAY" link set "$VETH_RELAY_OUT_A" up
${SUDO} ip -n "$NS_RELAY" link set "$VETH_RELAY_OUT_B" up
${SUDO} ip -n "$NS_SINK_A" link set "$VETH_SINK_A" up
${SUDO} ip -n "$NS_SINK_B" link set "$VETH_SINK_B" up

log "configure routes"
${SUDO} ip -n "$NS_DEVICE" route add 10.20.2.0/24 via 10.20.1.1
${SUDO} ip -n "$NS_DEVICE" route add 10.20.3.0/24 via 10.20.1.1
${SUDO} ip -n "$NS_DEVICE" route add 10.20.4.0/24 via 10.20.1.1
${SUDO} ip -n "$NS_EDGE" route add 10.20.3.0/24 via 10.20.2.2
${SUDO} ip -n "$NS_EDGE" route add 10.20.4.0/24 via 10.20.2.2
${SUDO} ip -n "$NS_SINK_A" route add 10.20.1.0/24 via 10.20.3.1
${SUDO} ip -n "$NS_SINK_A" route add 10.20.2.0/24 via 10.20.3.1
${SUDO} ip -n "$NS_SINK_B" route add 10.20.1.0/24 via 10.20.4.1
${SUDO} ip -n "$NS_SINK_B" route add 10.20.2.0/24 via 10.20.4.1

log "done"
${SUDO} ip netns list
