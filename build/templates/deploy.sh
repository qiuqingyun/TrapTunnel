#!/usr/bin/env bash
set -euo pipefail

COMPONENT="{{COMPONENT_NAME}}"
SERVICE_NAME="traptunnel-${COMPONENT}"
SERVICE_TEMPLATE="traptunnel-${COMPONENT}.service"
DEFAULT_INSTALL_DIR="/opt/traptunnel/${COMPONENT}"
CONFIG_FILE="{{CONFIG_FILE}}"
BINARY_NAME="{{EXECUTABLE_NAME}}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

default_service_user() {
  if [[ -n "${SUDO_USER:-}" ]]; then
    printf "%s" "$SUDO_USER"
  else
    whoami
  fi
}

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

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)
      TARGET_ARCH="amd64"
      EXPECTED_MACHINE="62"
      ;;
    aarch64|arm64|armv8l|armv8*)
      TARGET_ARCH="arm64"
      EXPECTED_MACHINE="183"
      ;;
    *)
      fail "不支持的架构: $(uname -m)"
      ;;
  esac
}

usage() {
  printf "Usage: %s [--install-dir=PATH] [--user=USER]\n" "$0"
}

INSTALL_DIR="${INSTALL_DIR:-$DEFAULT_INSTALL_DIR}"
SERVICE_USER="${SERVICE_USER:-$(default_service_user)}"

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
  fail "需要 root 权限执行部署"
fi

for cmd in systemctl mkdir cp mv sed id ln dd od tr uname install chown chmod readlink cmp rm; do
  command -v "$cmd" >/dev/null 2>&1 || fail "缺少依赖命令: $cmd"
done

if ! id -u "$SERVICE_USER" >/dev/null 2>&1; then
  fail "用户 $SERVICE_USER 不存在，请先创建后再部署，或使用 --user 指定已有用户"
fi

detect_arch
BINARY_SRC="$SCRIPT_DIR/${COMPONENT}-${TARGET_ARCH}"
[[ -f "$BINARY_SRC" ]] || fail "未找到架构 ${TARGET_ARCH} 对应的二进制: $BINARY_SRC"
validate_binary_arch "$BINARY_SRC" "$EXPECTED_MACHINE"
log "检测到目标架构 $(uname -m)，将使用 ${COMPONENT}-${TARGET_ARCH}"

timestamp="$(date +%Y%m%d%H%M%S)"
backup_dir=""
service_path="/etc/systemd/system/${SERVICE_NAME}.service"

if [[ -d "$INSTALL_DIR" ]]; then
  backup_dir="${INSTALL_DIR}.bak-${timestamp}"
  mv "$INSTALL_DIR" "$backup_dir"
  ln -sfn "$backup_dir" "${INSTALL_DIR}.bak-current"
  log "已备份旧安装目录到 $backup_dir"
fi

if [[ -f "$service_path" ]]; then
  service_backup="${service_path}.bak-${timestamp}"
  cp "$service_path" "$service_backup"
  ln -sfn "$service_backup" "${service_path}.bak-current"
  log "已备份旧 service 文件到 $service_backup"
fi

legacy_path="/usr/local/bin/${COMPONENT}"
if [[ -n "$backup_dir" && -f "$legacy_path" && -f "$backup_dir/${COMPONENT}" ]]; then
  if cmp -s "$legacy_path" "$backup_dir/${COMPONENT}"; then
    rm -f "$legacy_path"
    log "已清理旧命令: $legacy_path"
  fi
fi

mkdir -p "$INSTALL_DIR"
mkdir -p "$INSTALL_DIR/examples" "$INSTALL_DIR/legacy-configs" "$INSTALL_DIR/reference-docs"

install -m 0755 "$BINARY_SRC" "$INSTALL_DIR/${BINARY_NAME}"
install -m 0755 "$SCRIPT_DIR/${COMPONENT}-amd64" "$INSTALL_DIR/${COMPONENT}-amd64"
install -m 0755 "$SCRIPT_DIR/${COMPONENT}-arm64" "$INSTALL_DIR/${COMPONENT}-arm64"
install -m 0755 "$SCRIPT_DIR/start.sh" "$INSTALL_DIR/start.sh"
install -m 0755 "$SCRIPT_DIR/stop.sh" "$INSTALL_DIR/stop.sh"
install -m 0755 "$SCRIPT_DIR/restart.sh" "$INSTALL_DIR/restart.sh"
install -m 0755 "$SCRIPT_DIR/rollback.sh" "$INSTALL_DIR/rollback.sh"
install -m 0755 "$SCRIPT_DIR/verify.sh" "$INSTALL_DIR/verify.sh"
install -m 0755 "$SCRIPT_DIR/uninstall.sh" "$INSTALL_DIR/uninstall.sh"
install -m 0644 "$SCRIPT_DIR/${SERVICE_TEMPLATE}" "$INSTALL_DIR/${SERVICE_TEMPLATE}"
install -m 0644 "$SCRIPT_DIR/DEPLOYMENT.txt" "$INSTALL_DIR/DEPLOYMENT.txt"
install -m 0644 "$SCRIPT_DIR/TEST_AND_OBSERVE.txt" "$INSTALL_DIR/TEST_AND_OBSERVE.txt"
install -m 0644 "$SCRIPT_DIR/CONFIG_MIGRATION.txt" "$INSTALL_DIR/CONFIG_MIGRATION.txt"
install -m 0644 "$SCRIPT_DIR/${CONFIG_FILE}" "$INSTALL_DIR/${CONFIG_FILE}.template"

if [[ -d "$SCRIPT_DIR/examples" ]]; then
  cp -R "$SCRIPT_DIR/examples/." "$INSTALL_DIR/examples/"
fi
if [[ -d "$SCRIPT_DIR/legacy-configs" ]]; then
  cp -R "$SCRIPT_DIR/legacy-configs/." "$INSTALL_DIR/legacy-configs/"
fi
if [[ -d "$SCRIPT_DIR/reference-docs" ]]; then
  cp -R "$SCRIPT_DIR/reference-docs/." "$INSTALL_DIR/reference-docs/"
fi

if [[ -n "$backup_dir" && -f "$backup_dir/${CONFIG_FILE}" ]]; then
  cp "$backup_dir/${CONFIG_FILE}" "$INSTALL_DIR/${CONFIG_FILE}"
  log "已保留旧配置文件: $INSTALL_DIR/${CONFIG_FILE}"
else
  install -m 0644 "$SCRIPT_DIR/${CONFIG_FILE}" "$INSTALL_DIR/${CONFIG_FILE}"
  log "已写入默认配置模板: $INSTALL_DIR/${CONFIG_FILE}"
fi

install -m 0755 "$INSTALL_DIR/${BINARY_NAME}" "/usr/local/bin/${BINARY_NAME}"

SERVICE_GROUP="$(id -gn "$SERVICE_USER")"
LOG_DIR="/var/log/traptunnel"
mkdir -p "$LOG_DIR"
chown "$SERVICE_USER:$SERVICE_GROUP" "$LOG_DIR"
chmod 755 "$LOG_DIR"

sed -e "s#{{INSTALL_DIR}}#${INSTALL_DIR}#g" \
    -e "s#{{SERVICE_USER}}#${SERVICE_USER}#g" \
    -e "s#{{SERVICE_GROUP}}#${SERVICE_GROUP}#g" \
    "$SCRIPT_DIR/${SERVICE_TEMPLATE}" > "$service_path"

if [[ "$SERVICE_USER" != "root" ]]; then
  chown -R "$SERVICE_USER:$SERVICE_GROUP" "$INSTALL_DIR"
fi

if [[ -x "/sbin/restorecon" ]]; then
  /sbin/restorecon -v "$INSTALL_DIR/${BINARY_NAME}" "$service_path" || true
fi

systemctl daemon-reload
systemctl enable "$SERVICE_NAME"
systemctl restart "$SERVICE_NAME"

log "部署完成: ${SERVICE_NAME}"
log "安装目录: $INSTALL_DIR"
log "命令路径: /usr/local/bin/${BINARY_NAME}"
log "配置文件: $INSTALL_DIR/${CONFIG_FILE}"
log "验证命令: sudo $INSTALL_DIR/verify.sh"
