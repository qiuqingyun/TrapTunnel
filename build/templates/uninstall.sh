#!/usr/bin/env bash
set -euo pipefail

COMPONENT="{{COMPONENT_NAME}}"
SERVICE_NAME="traptunnel-${COMPONENT}"
DEFAULT_INSTALL_DIR="/opt/${COMPONENT}"

log_time() { date +"%Y-%m-%d %H:%M:%S"; }
log() { printf "[%s] %s\n" "$(log_time)" "$*"; }
fail() { log "ERROR: $*"; exit 1; }
on_error() { log "ERROR: command failed at line $1"; exit 1; }
trap 'on_error $LINENO' ERR

usage() {
  printf "Usage: %s [--install-dir=PATH]\n" "$0"
}

INSTALL_DIR="${INSTALL_DIR:-$DEFAULT_INSTALL_DIR}"

for arg in "$@"; do
  case "$arg" in
    --install-dir=*)
      INSTALL_DIR="${arg#*=}"
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      fail "unknown argument: $arg"
      ;;
  esac
done

if [[ "$(id -u)" -ne 0 ]]; then
  fail "需要 root 权限执行卸载"
fi

systemctl stop "$SERVICE_NAME" || true
systemctl disable "$SERVICE_NAME" || true

service_path="/etc/systemd/system/${SERVICE_NAME}.service"
rm -f "$service_path"
rm -f "${service_path}.bak-current"

systemctl daemon-reload

rm -f "/usr/local/bin/${COMPONENT}"
rm -rf "$INSTALL_DIR"
rm -f "${INSTALL_DIR}.bak-current"

log "卸载完成: ${SERVICE_NAME}"
