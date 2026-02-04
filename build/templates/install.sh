#!/usr/bin/env bash
set -euo pipefail

COMPONENT="{{COMPONENT_NAME}}"
SERVICE_NAME="traptunnel-${COMPONENT}"
SERVICE_TEMPLATE="traptunnel-${COMPONENT}.service"
DEFAULT_INSTALL_DIR="/opt/${COMPONENT}"
DEFAULT_USER="traptunnel-${COMPONENT}"

log_time() { date +"%Y-%m-%d %H:%M:%S"; }
log() { printf "[%s] %s\n" "$(log_time)" "$*"; }
fail() { log "ERROR: $*"; exit 1; }
on_error() { log "ERROR: command failed at line $1"; exit 1; }
trap 'on_error $LINENO' ERR

usage() {
  printf "Usage: %s [--install-dir=PATH] [--user=USER]\n" "$0"
}

INSTALL_DIR="${INSTALL_DIR:-$DEFAULT_INSTALL_DIR}"
SERVICE_USER="${SERVICE_USER:-$DEFAULT_USER}"

for arg in "$@"; do
  case "$arg" in
    --install-dir=*)
      INSTALL_DIR="${arg#*=}"
      ;;
    --user=*)
      SERVICE_USER="${arg#*=}"
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
  fail "需要 root 权限执行安装"
fi

for cmd in systemctl mkdir cp mv sed id ln; do
  command -v "$cmd" >/dev/null 2>&1 || fail "缺少依赖命令: $cmd"
done

if [[ "$SERVICE_USER" != "root" ]]; then
  if ! id -u "$SERVICE_USER" >/dev/null 2>&1; then
    if command -v useradd >/dev/null 2>&1; then
      useradd --system --no-create-home --shell /sbin/nologin "$SERVICE_USER" || useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"
    elif command -v adduser >/dev/null 2>&1; then
      adduser --system --no-create-home --shell /sbin/nologin "$SERVICE_USER" || adduser --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"
    else
      fail "缺少 useradd 或 adduser"
    fi
    log "创建用户 $SERVICE_USER"
  fi
fi

timestamp="$(date +%Y%m%d%H%M%S)"

if [[ -d "$INSTALL_DIR" ]]; then
  backup_dir="${INSTALL_DIR}.bak-${timestamp}"
  mv "$INSTALL_DIR" "$backup_dir"
  ln -sfn "$backup_dir" "${INSTALL_DIR}.bak-current"
  log "已备份旧版本到 $backup_dir"
fi

service_path="/etc/systemd/system/${SERVICE_NAME}.service"
if [[ -f "$service_path" ]]; then
  service_backup="${service_path}.bak-${timestamp}"
  cp "$service_path" "$service_backup"
  ln -sfn "$service_backup" "${service_path}.bak-current"
  log "已备份旧服务文件到 $service_backup"
fi

mkdir -p "$INSTALL_DIR"

# 检测架构并选择对应的二进制文件
ARCH=$(uname -m)
case $ARCH in
    x86_64)
        BINARY_SRC="./${COMPONENT}-amd64"
        ;;
    aarch64)
        BINARY_SRC="./${COMPONENT}-arm64"
        ;;
    *)
        fail "不支持的架构: $ARCH"
        ;;
esac

if [[ ! -f "$BINARY_SRC" ]]; then
    fail "未找到架构 $ARCH 对应的二进制文件: $BINARY_SRC"
fi

log "检测到架构 $ARCH，使用二进制文件: $BINARY_SRC"
cp "$BINARY_SRC" "$INSTALL_DIR/${COMPONENT}"
chmod 755 "$INSTALL_DIR/${COMPONENT}"

if [[ ! -f "$INSTALL_DIR/${COMPONENT}.conf" ]]; then
  cp "./${COMPONENT}.conf" "$INSTALL_DIR/${COMPONENT}.conf"
else
  log "保留现有配置文件 $INSTALL_DIR/${COMPONENT}.conf"
fi

cp "$INSTALL_DIR/${COMPONENT}" "/usr/local/bin/${COMPONENT}"

sed -e "s#{{INSTALL_DIR}}#${INSTALL_DIR}#g" -e "s#{{SERVICE_USER}}#${SERVICE_USER}#g" "./${SERVICE_TEMPLATE}" > "$service_path"

if [[ "$SERVICE_USER" != "root" ]]; then
  chown -R "$SERVICE_USER:$SERVICE_USER" "$INSTALL_DIR"
fi

systemctl daemon-reload
systemctl enable "$SERVICE_NAME"
systemctl restart "$SERVICE_NAME"
log "安装完成: ${SERVICE_NAME}"
