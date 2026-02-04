#!/usr/bin/env bash
set -euo pipefail

COMPONENT="{{COMPONENT_NAME}}"
SERVICE_NAME="traptunnel-${COMPONENT}"

log_time() { date +"%Y-%m-%d %H:%M:%S"; }
log() { printf "[%s] %s\n" "$(log_time)" "$*"; }
fail() { log "ERROR: $*"; exit 1; }
on_error() { log "ERROR: command failed at line $1"; exit 1; }
trap 'on_error $LINENO' ERR

if [[ "$(id -u)" -ne 0 ]]; then
  fail "需要 root 权限执行操作"
fi

systemctl restart "$SERVICE_NAME"
log "restart 执行完成: ${SERVICE_NAME}"
