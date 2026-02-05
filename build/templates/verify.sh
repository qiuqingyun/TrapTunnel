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
CONF="${INSTALL_DIR}/${COMPONENT}.conf"

if [[ "$(id -u)" -ne 0 ]]; then
  fail "需要 root 权限执行验证"
fi

systemctl is-active --quiet "$SERVICE_NAME" || fail "服务未运行: $SERVICE_NAME"
log "服务状态正常: ${SERVICE_NAME}"

if [[ ! -f "$CONF" ]]; then
  fail "未找到配置文件: $CONF"
fi

port="$(awk -F'=' 'tolower($1) ~ /listen_port/ {gsub(/ /,"",$2); print $2; exit}' "$CONF")"
if [[ -z "$port" ]]; then
  fail "无法从配置获取 listen_port"
fi

if command -v ss >/dev/null 2>&1; then
  if [[ "$COMPONENT" == "receiver" ]]; then
    ss -ltn | awk '{print $4}' | grep -E ":${port}$" >/dev/null 2>&1 || fail "未检测到监听端口: $port"
  else
    ss -lun | awk '{print $4}' | grep -E ":${port}$" >/dev/null 2>&1 || fail "未检测到监听端口: $port"
  fi
elif command -v netstat >/dev/null 2>&1; then
  if [[ "$COMPONENT" == "receiver" ]]; then
    netstat -lnt | awk '{print $4}' | grep -E ":${port}$" >/dev/null 2>&1 || fail "未检测到监听端口: $port"
  else
    netstat -lun | awk '{print $4}' | grep -E ":${port}$" >/dev/null 2>&1 || fail "未检测到监听端口: $port"
  fi
else
  fail "缺少 ss 或 netstat"
fi

log "端口监听正常: ${port}"
