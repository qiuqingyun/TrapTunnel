#!/usr/bin/env bash
set -euo pipefail

COMPONENT="{{COMPONENT_NAME}}"
SERVICE_NAME="traptunnel-${COMPONENT}"
DEFAULT_INSTALL_DIR="/opt/traptunnel/${COMPONENT}"
CONFIG_FILE="{{CONFIG_FILE}}"

log_time() { date +"%Y-%m-%d %H:%M:%S"; }
log() { printf "[%s] %s\n" "$(log_time)" "$*"; }
fail() { log "ERROR: $*"; exit 1; }
on_error() { log "ERROR: command failed at line $1"; exit 1; }
trap 'on_error $LINENO' ERR

INSTALL_DIR="${INSTALL_DIR:-$DEFAULT_INSTALL_DIR}"
CONF="${INSTALL_DIR}/${CONFIG_FILE}"

if [[ "$(id -u)" -ne 0 ]]; then
  fail "需要 root 权限执行验证"
fi

systemctl is-active --quiet "$SERVICE_NAME" || fail "服务未运行: $SERVICE_NAME"
log "服务状态正常: ${SERVICE_NAME}"

if [[ ! -f "$CONF" ]]; then
  fail "未找到配置文件: $CONF"
fi

check_tcp_port() {
  local port="$1"
  if command -v ss >/dev/null 2>&1; then
    ss -ltn | awk '{print $4}' | grep -E ":${port}$" >/dev/null 2>&1 || fail "未检测到 TCP 监听端口: $port"
  elif command -v netstat >/dev/null 2>&1; then
    netstat -lnt | awk '{print $4}' | grep -E ":${port}$" >/dev/null 2>&1 || fail "未检测到 TCP 监听端口: $port"
  else
    fail "缺少 ss 或 netstat"
  fi
}

check_udp_port() {
  local port="$1"
  if command -v ss >/dev/null 2>&1; then
    ss -lun | awk '{print $5}' | grep -E ":${port}$" >/dev/null 2>&1 || fail "未检测到 UDP 监听端口: $port"
  elif command -v netstat >/dev/null 2>&1; then
    netstat -lun | awk '{print $4}' | grep -E ":${port}$" >/dev/null 2>&1 || fail "未检测到 UDP 监听端口: $port"
  else
    fail "缺少 ss 或 netstat"
  fi
}

if [[ "$COMPONENT" == "node" ]]; then
  ingress_port="$(awk -F'=' '
    /^\[ingress\]/ {section="ingress"; next}
    /^\[/ {section=""}
    section=="ingress" && $1 ~ /listen/ {
      gsub(/ /, "", $2)
      sub(/.*:/, "", $2)
      print $2
      exit
    }' "$CONF")"

  capture_port="$(awk -F'=' '
    /^\[capture\]/ {section="capture"; next}
    /^\[/ {section=""}
    section=="capture" && $1 ~ /listen_ports/ {
      gsub(/ /, "", $2)
      gsub(/\[/, "", $2)
      gsub(/\]/, "", $2)
      split($2, parts, ",")
      print parts[1]
      exit
    }' "$CONF")"

  if [[ -n "$ingress_port" ]]; then
    check_tcp_port "$ingress_port"
    log "TCP 监听正常: ${ingress_port}"
  fi
  if [[ -n "$capture_port" ]]; then
    log "已读取 capture.listen_ports=${capture_port}；raw socket 场景不通过 ss/netstat 做精确校验"
  fi

  if [[ -z "$ingress_port" && -z "$capture_port" ]]; then
    fail "未能从 node.toml 识别 ingress.listen 或 capture.listen_ports"
  fi
else
  port="$(awk -F'=' 'tolower($1) ~ /listen_port/ {gsub(/ /,"",$2); print $2; exit}' "$CONF")"
  if [[ -z "$port" ]]; then
    fail "无法从配置获取 listen_port"
  fi

  if [[ "$COMPONENT" == "receiver" ]]; then
    check_tcp_port "$port"
  else
    check_udp_port "$port"
  fi
  log "端口监听正常: ${port}"
fi
