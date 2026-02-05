#!/usr/bin/env bash
set -euo pipefail

COMPONENT="{{COMPONENT_NAME}}"
SERVICE_NAME="traptunnel-${COMPONENT}"
DEFAULT_INSTALL_DIR="/opt/traptunnel/${COMPONENT}"

log_time() { date +"%Y-%m-%d %H:%M:%S"; }
log() { printf "[%s] %s\n" "$(log_time)" "$*"; }
fail() { log "ERROR: $*"; exit 1; }
on_error() { log "ERROR: command failed at line $1"; exit 1; }
trap 'on_error $LINENO' ERR

INSTALL_DIR="${INSTALL_DIR:-$DEFAULT_INSTALL_DIR}"
backup_link="${INSTALL_DIR}.bak-current"
service_path="/etc/systemd/system/${SERVICE_NAME}.service"
service_backup_link="${service_path}.bak-current"

if [[ "$(id -u)" -ne 0 ]]; then
  fail "需要 root 权限执行回滚"
fi

if [[ ! -L "$backup_link" ]]; then
  fail "未找到可用备份: $backup_link"
fi

backup_dir="$(readlink -f "$backup_link")"
if [[ ! -d "$backup_dir" ]]; then
  fail "备份目录不存在: $backup_dir"
fi

timestamp="$(date +%Y%m%d%H%M%S)"
systemctl stop "$SERVICE_NAME" || true

if [[ -d "$INSTALL_DIR" ]]; then
  mv "$INSTALL_DIR" "${INSTALL_DIR}.failed-${timestamp}"
fi

mv "$backup_dir" "$INSTALL_DIR"
rm -f "$backup_link"

if [[ -L "$service_backup_link" ]]; then
  service_backup="$(readlink -f "$service_backup_link")"
  if [[ -f "$service_backup" ]]; then
    cp "$service_backup" "$service_path"
  fi
fi

systemctl daemon-reload
systemctl restart "$SERVICE_NAME"
log "回滚完成: ${SERVICE_NAME}"
