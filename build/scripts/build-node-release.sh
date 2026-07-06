#!/usr/bin/env bash
set -euo pipefail

log_time() {
  date +"%Y-%m-%d %H:%M:%S"
}

log_info() {
  printf "[%s] [INFO] %s\n" "$(log_time)" "$*"
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
OUTPUT_DIR="$BUILD_DIR/releases"
COMPONENT="node"
EXECUTABLE_NAME="traptunnel-${COMPONENT}"

usage() {
  printf "Usage: %s [--clean]\n" "$0"
}

CLEAN=false
for arg in "$@"; do
  case "$arg" in
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

GO_CMD="go"

check_deps() {
  if ! command -v go >/dev/null 2>&1; then
    if command -v go.exe >/dev/null 2>&1; then
      GO_CMD="go.exe"
    elif [[ -x "$HOME/.local/go/bin/go" ]]; then
      GO_CMD="$HOME/.local/go/bin/go"
    else
      fail "missing dependency: go"
    fi
  fi

  local deps=("tar" "gzip" "sed" "find" "dd" "od" "tr" "sha256sum" "install")
  for d in "${deps[@]}"; do
    command -v "$d" >/dev/null 2>&1 || fail "missing dependency: $d"
  done

  command -v python3 >/dev/null 2>&1 || fail "missing dependency: python3"
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

needs_build() {
  local arch="$1"
  local output="$BUILD_DIR/${COMPONENT}/${COMPONENT}-linux-${arch}"
  if [[ ! -f "$output" ]]; then
    return 0
  fi
  if [[ "$ROOT_DIR/go.mod" -nt "$output" || "$ROOT_DIR/go.sum" -nt "$output" ]]; then
    return 0
  fi
  if find "$ROOT_DIR/internal" -type f -newer "$output" -print -quit | grep -q .; then
    return 0
  fi
  if find "$ROOT_DIR/cmd/node" -type f -newer "$output" -print -quit | grep -q .; then
    return 0
  fi
  return 1
}

build_arch() {
  local arch="$1"
  local output_dir="$BUILD_DIR/${COMPONENT}"
  local output="$output_dir/${COMPONENT}-linux-${arch}"
  mkdir -p "$output_dir"
  if needs_build "$arch"; then
    log_info "building ${COMPONENT} linux/${arch}"
    (cd "$ROOT_DIR" && GOOS=linux GOARCH="$arch" CGO_ENABLED=0 "$GO_CMD" build -o "build/${COMPONENT}/${COMPONENT}-linux-${arch}" "./cmd/node")
    validate_binary_arch "$output" "$arch"
  else
    log_info "skip build ${COMPONENT} linux/${arch} (no changes)"
  fi
}

copy_examples() {
  local target_dir="$1"
  mkdir -p "$target_dir/examples"
  cp "$ROOT_DIR"/examples/*.toml "$target_dir/examples/"
}

copy_legacy_examples() {
  local target_dir="$1"
  mkdir -p "$target_dir/legacy-configs"
  cp "$TEMPLATE_DIR/sender.conf" "$target_dir/legacy-configs/"
  cp "$TEMPLATE_DIR/receiver.conf" "$target_dir/legacy-configs/"
}

copy_reference_docs() {
  local target_dir="$1"
  mkdir -p "$target_dir/reference-docs"
  cp "$ROOT_DIR/README.md" "$target_dir/reference-docs/"
  cp "$ROOT_DIR/docs/manual-config-migration.md" "$target_dir/reference-docs/"
  cp "$ROOT_DIR/docs/local-node-test-environment.md" "$target_dir/reference-docs/"
}

render_templates() {
  local pkg_dir="$1"
  cp "$TEMPLATE_DIR/node.toml" "$pkg_dir/node.toml"
  cp "$TEMPLATE_DIR/traptunnel.service" "$pkg_dir/traptunnel-node.service"
  sed -i "s/{{COMPONENT_NAME}}/${COMPONENT}/g" "$pkg_dir/traptunnel-node.service"
  sed -i "s/{{EXECUTABLE_NAME}}/${EXECUTABLE_NAME}/g" "$pkg_dir/traptunnel-node.service"
  sed -i "s/{{CONFIG_FILE}}/node.toml/g" "$pkg_dir/traptunnel-node.service"

  local scripts=("deploy.sh" "install.sh" "start.sh" "stop.sh" "restart.sh" "rollback.sh" "verify.sh" "uninstall.sh")
  for script in "${scripts[@]}"; do
    cp "$TEMPLATE_DIR/$script" "$pkg_dir/$script"
    sed -i "s/{{COMPONENT_NAME}}/${COMPONENT}/g" "$pkg_dir/$script"
    sed -i "s/{{EXECUTABLE_NAME}}/${EXECUTABLE_NAME}/g" "$pkg_dir/$script"
    sed -i "s/{{CONFIG_FILE}}/node.toml/g" "$pkg_dir/$script"
    chmod +x "$pkg_dir/$script"
  done

  local docs=("DEPLOYMENT.txt" "TEST_AND_OBSERVE.txt" "CONFIG_MIGRATION.txt")
  for doc in "${docs[@]}"; do
    cp "$TEMPLATE_DIR/$doc" "$pkg_dir/$doc"
    sed -i "s/{{COMPONENT_NAME}}/${COMPONENT}/g" "$pkg_dir/$doc"
    sed -i "s/{{EXECUTABLE_NAME}}/${EXECUTABLE_NAME}/g" "$pkg_dir/$doc"
  done
}

write_release_info() {
  local pkg_dir="$1"
  local version="0.0.0"
  local commit="unknown"
  if command -v git >/dev/null 2>&1; then
    version="$(git -C "$ROOT_DIR" describe --tags --always 2>/dev/null || printf "0.0.0")"
    commit="$(git -C "$ROOT_DIR" rev-parse --short HEAD 2>/dev/null || printf "unknown")"
  fi
  cat > "$pkg_dir/RELEASE_INFO.txt" <<EOF
component=${COMPONENT}
version=${version}
commit=${commit}
built_at=$(date +"%Y-%m-%d %H:%M:%S %z")
EOF
}

write_checksums() {
  local pkg_dir="$1"
  (
    cd "$pkg_dir"
    find . -type f ! -name 'SHA256SUMS.txt' -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS.txt
  )
}

write_filelist() {
  local pkg_dir="$1"
  (
    cd "$pkg_dir"
    find . -type f | sort > PACKAGE_CONTENTS.txt
  )
}

make_archives() {
  local staging_root="$1"
  local pkg_name="$2"
  mkdir -p "$OUTPUT_DIR"
  tar -czf "$OUTPUT_DIR/${pkg_name}.tar.gz" -C "$staging_root" "$pkg_name"
  (
    cd "$staging_root"
    python3 -m zipfile -c "$OUTPUT_DIR/${pkg_name}.zip" "$pkg_name"
  )
  log_info "created $OUTPUT_DIR/${pkg_name}.tar.gz"
  log_info "created $OUTPUT_DIR/${pkg_name}.zip"
}

main() {
  check_deps

  if $CLEAN; then
    rm -rf "$OUTPUT_DIR" "$BUILD_DIR/.staging-release"
  fi

  build_arch "amd64"
  build_arch "arm64"

  local timestamp
  timestamp="$(date +"%Y%m%d-%H%M%S")"
  local pkg_name="traptunnel-node-prod-linux-${timestamp}"
  local staging_root="$BUILD_DIR/.staging-release"
  local pkg_dir="$staging_root/$pkg_name"

  rm -rf "$staging_root"
  mkdir -p "$pkg_dir"

  cp "$BUILD_DIR/${COMPONENT}/${COMPONENT}-linux-amd64" "$pkg_dir/${COMPONENT}-amd64"
  cp "$BUILD_DIR/${COMPONENT}/${COMPONENT}-linux-arm64" "$pkg_dir/${COMPONENT}-arm64"
  chmod +x "$pkg_dir/${COMPONENT}-amd64" "$pkg_dir/${COMPONENT}-arm64"

  render_templates "$pkg_dir"
  copy_examples "$pkg_dir"
  copy_legacy_examples "$pkg_dir"
  copy_reference_docs "$pkg_dir"
  write_release_info "$pkg_dir"
  write_filelist "$pkg_dir"
  write_checksums "$pkg_dir"
  make_archives "$staging_root" "$pkg_name"

  rm -rf "$staging_root"
}

main
