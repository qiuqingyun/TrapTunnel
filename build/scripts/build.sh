#!/usr/bin/env bash
set -euo pipefail

log_time() {
  date +"%Y-%m-%d %H:%M:%S"
}

log_info() {
  printf "[%s] [INFO] %s\n" "$(log_time)" "$*"
}

log_warn() {
  printf "[%s] [WARN] %s\n" "$(log_time)" "$*"
}

log_error() {
  printf "[%s] [ERROR] %s\n" "$(log_time)" "$*"
}

fail() {
  log_error "$*"
  exit 1
}

on_error() {
  log_error "command failed at line $1"
  exit 1
}

trap 'on_error $LINENO' ERR

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
BUILD_DIR="$ROOT_DIR/build"
TEMPLATE_DIR="$BUILD_DIR/templates"

COMPONENT="all"
CLEAN=false

usage() {
  printf "Usage: %s [--component=all|receiver|sender] [--clean]\n" "$0"
}

for arg in "$@"; do
  case "$arg" in
    --component=*)
      COMPONENT="${arg#*=}"
      ;;
    --clean)
      CLEAN=true
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

case "$COMPONENT" in
  all|receiver|sender) ;;
  *) fail "invalid component: $COMPONENT" ;;
esac

GO_CMD="go"

check_deps() {
  if ! command -v go >/dev/null 2>&1; then
    if command -v go.exe >/dev/null 2>&1; then
      GO_CMD="go.exe"
    elif [[ -x "/mnt/c/Program Files/Go/bin/go.exe" ]]; then
      GO_CMD="/mnt/c/Program Files/Go/bin/go.exe"
    else
      fail "missing dependency: go"
    fi
  fi

  local deps=("tar" "gzip" "sed" "find" "dd" "od" "tr")
  for d in "${deps[@]}"; do
    command -v "$d" >/dev/null 2>&1 || fail "missing dependency: $d"
  done
}

get_elf_machine() {
  local file="$1"
  local magic
  magic="$(dd if="$file" bs=1 count=4 2>/dev/null | od -An -t x1 | tr -d ' \n')"
  if [[ "$magic" != "7f454c46" ]]; then
    fail "invalid binary format (not ELF): $file"
  fi
  dd if="$file" bs=1 skip=18 count=2 2>/dev/null | od -An -t u2 | tr -d ' \n'
}

validate_binary_arch() {
  local file="$1"
  local arch="$2"
  local machine
  machine="$(get_elf_machine "$file")"
  case "$arch" in
    amd64)
      [[ "$machine" == "62" ]] || fail "binary arch mismatch: $file (expected amd64)"
      ;;
    arm64)
      [[ "$machine" == "183" ]] || fail "binary arch mismatch: $file (expected arm64)"
      ;;
    *)
      fail "unknown arch: $arch"
      ;;
  esac
}

get_version() {
  local version=""
  if command -v git >/dev/null 2>&1; then
    version="$(git -C "$ROOT_DIR" describe --tags --abbrev=0 2>/dev/null || true)"
  fi
  if [[ -z "$version" && -f "$ROOT_DIR/package.json" ]]; then
    version="$(grep -m1 '"version"' "$ROOT_DIR/package.json" | sed -E 's/.*"version"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/' || true)"
  fi
  if [[ -z "$version" ]]; then
    version="0.0.0"
  fi
  printf "%s" "${version#v}"
}

needs_build() {
  local component="$1"
  local arch="$2"
  local output="$BUILD_DIR/$component/${component}-linux-${arch}"
  if [[ ! -f "$output" ]]; then
    return 0
  fi
  if [[ "$ROOT_DIR/go.mod" -nt "$output" || "$ROOT_DIR/go.sum" -nt "$output" ]]; then
    return 0
  fi
  if find "$ROOT_DIR/$component" -type f -newer "$output" -print -quit | grep -q .; then
    return 0
  fi
  return 1
}

build_component_arch() {
  local component="$1"
  local arch="$2"
  local output_dir="$BUILD_DIR/$component"
  local output="$output_dir/${component}-linux-${arch}"
  mkdir -p "$output_dir"
  if needs_build "$component" "$arch"; then
    log_info "building $component linux/$arch"
    (cd "$ROOT_DIR" && GOOS=linux GOARCH="$arch" CGO_ENABLED=0 "$GO_CMD" build -o "build/$component/${component}-linux-${arch}" "./$component")
    validate_binary_arch "$output" "$arch"
  else
    log_info "skip build $component linux/$arch (no changes)"
  fi
}

package_component_combined() {
  local component="$1"
  local output_dir="$BUILD_DIR/$component"
  local bin_amd64="$output_dir/${component}-linux-amd64"
  local bin_arm64="$output_dir/${component}-linux-arm64"
  
  [[ -f "$bin_amd64" ]] || fail "missing binary: $bin_amd64"
  [[ -f "$bin_arm64" ]] || fail "missing binary: $bin_arm64"
  validate_binary_arch "$bin_amd64" "amd64"
  validate_binary_arch "$bin_arm64" "arm64"

  local version
  version="$(get_version)"
  
  local timestamp
  timestamp="$(date +"%Y%m%d-%H%M%S")"
  
  local staging_root="$BUILD_DIR/.staging/$component/combined"
  local pkg_name="${component}-linux-${timestamp}"
  local pkg_dir="$staging_root/$pkg_name"

  rm -rf "$staging_root"
  mkdir -p "$pkg_dir"

  # 复制并重命名二进制文件
  cp "$bin_amd64" "$pkg_dir/${component}-amd64"
  cp "$bin_arm64" "$pkg_dir/${component}-arm64"
  # 确保二进制文件有执行权限
  chmod +x "$pkg_dir/${component}-amd64" "$pkg_dir/${component}-arm64"

  # 复制并处理配置文件
  if [[ -f "$TEMPLATE_DIR/${component}.conf" ]]; then
    cp "$TEMPLATE_DIR/${component}.conf" "$pkg_dir/${component}.conf"
  else
    log_warn "template not found: $TEMPLATE_DIR/${component}.conf"
  fi

  # 复制并处理服务文件
  if [[ -f "$TEMPLATE_DIR/traptunnel.service" ]]; then
    cp "$TEMPLATE_DIR/traptunnel.service" "$pkg_dir/traptunnel-${component}.service"
    sed -i "s/{{COMPONENT_NAME}}/$component/g" "$pkg_dir/traptunnel-${component}.service"
  else
    log_warn "template not found: $TEMPLATE_DIR/traptunnel.service"
  fi

  # 复制并处理部署文档
  if [[ -f "$TEMPLATE_DIR/DEPLOYMENT.txt" ]]; then
    cp "$TEMPLATE_DIR/DEPLOYMENT.txt" "$pkg_dir/DEPLOYMENT.txt"
    sed -i "s/{{COMPONENT_NAME}}/$component/g" "$pkg_dir/DEPLOYMENT.txt"
  fi

  # 复制并处理脚本
  local scripts=("install.sh" "uninstall.sh" "start.sh" "stop.sh" "restart.sh" "rollback.sh" "verify.sh")
  for script in "${scripts[@]}"; do
    if [[ -f "$TEMPLATE_DIR/$script" ]]; then
      cp "$TEMPLATE_DIR/$script" "$pkg_dir/$script"
      sed -i "s/{{COMPONENT_NAME}}/$component/g" "$pkg_dir/$script"
      chmod +x "$pkg_dir/$script"
    else
      log_warn "template not found: $TEMPLATE_DIR/$script"
    fi
  done

  tar -czf "$output_dir/${pkg_name}.tar.gz" -C "$staging_root" "$pkg_name"
  log_info "packed $component linux combined version $version to $output_dir/${pkg_name}.tar.gz"
  rm -rf "$staging_root"
}

clean_build() {
  rm -rf "$BUILD_DIR/receiver" "$BUILD_DIR/sender" "$BUILD_DIR/.staging"
  log_info "build directory cleaned"
}

main() {
  check_deps
  mkdir -p "$BUILD_DIR/receiver" "$BUILD_DIR/sender"
  # 确保模板目录存在
  if [[ ! -d "$TEMPLATE_DIR" ]]; then
    fail "Template directory not found: $TEMPLATE_DIR"
  fi

  if $CLEAN; then
    clean_build
  fi

  local components=()
  if [[ "$COMPONENT" == "all" ]]; then
    components=("receiver" "sender")
  else
    components=("$COMPONENT")
  fi

  for c in "${components[@]}"; do
    # 强制构建所有架构
    build_component_arch "$c" "amd64"
    build_component_arch "$c" "arm64"
    
    # 打包合并版
    package_component_combined "$c"
  done

  log_info "all tasks completed"
}

main
