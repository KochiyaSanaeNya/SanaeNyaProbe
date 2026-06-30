#!/usr/bin/env bash
set -Eeuo pipefail

APP_NAME="SanaeNyaProbe"
AMD64_URL="https://github.com/KochiyaSanaeNya/SanaeNyaProbe/releases/download/0.0.1/SanaeNyaProbe_linux"
ARM64_URL="https://github.com/KochiyaSanaeNya/SanaeNyaProbe/releases/download/0.0.1/SanaeNyaProbe_arm64_linux"

BIN_PATH="/bin/${APP_NAME}"
CONFIG_DIR="/etc/${APP_NAME}"
CONFIG_PATH="${CONFIG_DIR}/probe.json"
SERVICE_PATH="/etc/systemd/system/${APP_NAME}.service"

tmp_file=""

cleanup() {
  if [[ -n "${tmp_file}" && -f "${tmp_file}" ]]; then
    rm -f "${tmp_file}"
  fi
}
trap cleanup EXIT

info() {
  printf '[INFO] %s\n' "$*"
}

die() {
  printf '[ERROR] %s\n' "$*" >&2
  exit 1
}

require_root() {
  if [[ "$(id -u)" -ne 0 ]]; then
    die "Please run as root, for example: sudo bash install.sh"
  fi
}

require_linux() {
  if [[ "$(uname -s)" != "Linux" ]]; then
    die "This installer only supports Linux."
  fi
}

command_exists() {
  command -v "$1" >/dev/null 2>&1
}

detect_download_url() {
  local machine
  machine="$(uname -m)"

  case "${machine}" in
    x86_64 | amd64)
      printf '%s\n' "${AMD64_URL}"
      ;;
    aarch64 | arm64)
      printf '%s\n' "${ARM64_URL}"
      ;;
    *)
      die "Unsupported architecture: ${machine}. Supported: amd64, arm64."
      ;;
  esac
}

download_binary() {
  local url="$1"

  tmp_file="$(mktemp)"

  info "Downloading ${APP_NAME} from ${url}"
  if command_exists curl; then
    curl -fL --retry 3 --connect-timeout 10 -o "${tmp_file}" "${url}"
  elif command_exists wget; then
    wget -O "${tmp_file}" "${url}"
  else
    die "curl or wget is required."
  fi

  if [[ ! -s "${tmp_file}" ]]; then
    die "Downloaded file is empty."
  fi

  install -m 0755 "${tmp_file}" "${BIN_PATH}"
  info "Installed binary to ${BIN_PATH}"
}

generate_uuid() {
  local hex

  if command_exists uuidgen; then
    uuidgen | tr '[:upper:]' '[:lower:]'
    return
  fi

  if [[ -r /proc/sys/kernel/random/uuid ]]; then
    cat /proc/sys/kernel/random/uuid
    return
  fi

  if [[ -r /dev/urandom ]] && command_exists od; then
    hex="$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
    printf '%s-%s-4%s-8%s-%s\n' \
      "${hex:0:8}" \
      "${hex:8:4}" \
      "${hex:13:3}" \
      "${hex:17:3}" \
      "${hex:20:12}"
    return
  fi

  die "Cannot generate UUID: uuidgen, /proc/sys/kernel/random/uuid, and /dev/urandom are unavailable."
}

json_escape() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  value="${value//$'\n'/\\n}"
  value="${value//$'\r'/\\r}"
  value="${value//$'\t'/\\t}"
  printf '%s' "${value}"
}

prompt_server_url() {
  local value

  while true; do
    read -r -p "server_url: " value
    value="${value#"${value%%[![:space:]]*}"}"
    value="${value%"${value##*[![:space:]]}"}"

    if [[ -z "${value}" ]]; then
      printf 'server_url cannot be empty.\n' >&2
      continue
    fi

    if [[ "${value}" != *"://"* ]]; then
      value="https://${value}"
    fi

    if [[ "${value}" != https://* ]]; then
      printf 'server_url must use https, or omit the scheme to default to https.\n' >&2
      continue
    fi

    printf '%s\n' "${value}"
    return
  done
}

prompt_name() {
  local default_name
  local value

  default_name="$(hostname 2>/dev/null || printf 'server-01')"

  while true; do
    read -r -p "name [${default_name}]: " value
    value="${value:-${default_name}}"
    value="${value#"${value%%[![:space:]]*}"}"
    value="${value%"${value##*[![:space:]]}"}"

    if [[ -z "${value}" ]]; then
      printf 'name cannot be empty.\n' >&2
      continue
    fi

    printf '%s\n' "${value}"
    return
  done
}

write_config() {
  local overwrite
  local server_url
  local name
  local uuid

  install -d -m 0755 "${CONFIG_DIR}"

  if [[ -f "${CONFIG_PATH}" ]]; then
    read -r -p "Config already exists at ${CONFIG_PATH}. Overwrite? [y/N]: " overwrite
    case "${overwrite}" in
      y | Y | yes | YES)
        ;;
      *)
        info "Keeping existing config: ${CONFIG_PATH}"
        return
        ;;
    esac
  fi

  server_url="$(prompt_server_url)"
  name="$(prompt_name)"
  uuid="$(generate_uuid)"

  cat >"${CONFIG_PATH}" <<EOF
{
  "server_url": "$(json_escape "${server_url}")",
  "name": "$(json_escape "${name}")",
  "uuid": "$(json_escape "${uuid}")"
}
EOF

  chmod 0644 "${CONFIG_PATH}"
  info "Wrote config to ${CONFIG_PATH}"
}

write_service() {
  cat >"${SERVICE_PATH}" <<EOF
[Unit]
Description=${APP_NAME}
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
ExecStart=${BIN_PATH} -config ${CONFIG_PATH}
Restart=always
RestartSec=5s
WorkingDirectory=/

[Install]
WantedBy=multi-user.target
EOF

  chmod 0644 "${SERVICE_PATH}"
  info "Wrote systemd service to ${SERVICE_PATH}"
}

enable_service() {
  if ! command_exists systemctl; then
    die "systemctl is required."
  fi

  systemctl daemon-reload
  systemctl enable "${APP_NAME}.service"
  systemctl restart "${APP_NAME}.service"
  info "Enabled and started ${APP_NAME}.service"
}

main() {
  local download_url

  require_linux
  require_root

  download_url="$(detect_download_url)"
  download_binary "${download_url}"
  write_config
  write_service
  enable_service

  info "Installation completed."
  info "Check status: systemctl status ${APP_NAME}.service"
}

main "$@"
