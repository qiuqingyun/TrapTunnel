#!/usr/bin/env bash
set -euo pipefail

COMPONENT="{{COMPONENT_NAME}}"
SERVICE_NAME="traptunnel-${COMPONENT}"
SERVICE_TEMPLATE="traptunnel-${COMPONENT}.service"
DEFAULT_INSTALL_DIR="/opt/${COMPONENT}"
if [ -n "${SUDO_USER:-}" ]; then
  DEFAULT_USER="$SUDO_USER"
else
  DEFAULT_USER="$(whoami)"
fi

log_time() { date +"%Y-%m-%d %H:%M:%S"; }
log() { printf "[%s] %s\n" "$(log_time)" "$*"; }
fail() { log "ERROR: $*"; exit 1; }
on_error() { log "ERROR: command failed at line $1"; exit 1; }
trap 'on_error $LINENO' ERR

get_elf_machine() {
  local file="$1"
  local magic
  magic="$(dd if="$file" bs=1 count=4 2>/dev/null | od -An -t x1 | tr -d ' \n')"
  if [[ "$magic" != "7f454c46" ]]; then
    fail "二进制格式错误(非ELF): $file"
  fi
  dd if="$file" bs=1 skip=18 count=2 2>/dev/null | od -An -t u2 | tr -d ' \n'
}

validate_binary_arch() {
  local file="$1"
  local expected="$2"
  local machine
  machine="$(get_elf_machine "$file")"
  if [[ "$machine" != "$expected" ]]; then
    fail "二进制架构不匹配: $file"
  fi
}

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

for cmd in systemctl mkdir cp mv sed id ln dd od tr; do
  command -v "$cmd" >/dev/null 2>&1 || fail "缺少依赖命令: $cmd"
done

if ! id -u "$SERVICE_USER" >/dev/null 2>&1; then
  fail "用户 $SERVICE_USER 不存在"
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
        EXPECTED_MACHINE="62"
        ;;
    aarch64)
        BINARY_SRC="./${COMPONENT}-arm64"
        EXPECTED_MACHINE="183"
        ;;
    *)
        fail "不支持的架构: $ARCH"
        ;;
esac

if [[ ! -f "$BINARY_SRC" ]]; then
    fail "未找到架构 $ARCH 对应的二进制文件: $BINARY_SRC"
fi
validate_binary_arch "$BINARY_SRC" "$EXPECTED_MACHINE"

log "检测到架构 $ARCH，使用二进制文件: $BINARY_SRC"
cp "$BINARY_SRC" "$INSTALL_DIR/${COMPONENT}"
chmod 755 "$INSTALL_DIR/${COMPONENT}"
# 确保二进制文件所有权正确
if [[ "$SERVICE_USER" != "root" ]]; then
    chown "$SERVICE_USER:$SERVICE_GROUP" "$INSTALL_DIR/${COMPONENT}"
fi

if [[ ! -f "$INSTALL_DIR/${COMPONENT}.conf" ]]; then
  cp "./${COMPONENT}.conf" "$INSTALL_DIR/${COMPONENT}.conf"
else
  log "保留现有配置文件 $INSTALL_DIR/${COMPONENT}.conf"
fi

cp "$INSTALL_DIR/${COMPONENT}" "/usr/local/bin/${COMPONENT}"

SERVICE_GROUP=$(id -gn "$SERVICE_USER")
log "配置服务运行用户: $SERVICE_USER (组: $SERVICE_GROUP)"

# 确保二进制文件在复制后也有正确的权限和SELinux上下文(如果启用)
if [[ -x "/sbin/restorecon" ]]; then
    /sbin/restorecon -v "$INSTALL_DIR/${COMPONENT}" || true
fi

# 确保日志目录存在并具有正确权限
LOG_DIR="/var/log/traptunnel"
if [[ ! -d "$LOG_DIR" ]]; then
  mkdir -p "$LOG_DIR"
  log "创建日志目录: $LOG_DIR"
fi
chown "$SERVICE_USER:$SERVICE_GROUP" "$LOG_DIR"
chmod 755 "$LOG_DIR"

sed -e "s#{{INSTALL_DIR}}#${INSTALL_DIR}#g" \
    -e "s#{{SERVICE_USER}}#${SERVICE_USER}#g" \
    -e "s#{{SERVICE_GROUP}}#${SERVICE_GROUP}#g" \
    "./${SERVICE_TEMPLATE}" > "$service_path"

if [[ "$SERVICE_USER" != "root" ]]; then
  chown -R "$SERVICE_USER:$SERVICE_GROUP" "$INSTALL_DIR"
fi

systemctl daemon-reload
systemctl enable "$SERVICE_NAME"
systemctl restart "$SERVICE_NAME"
log "安装完成: ${SERVICE_NAME}"
