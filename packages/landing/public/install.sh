#!/bin/sh
set -eu

DEFAULT_VERSION="v0.1.0-rc.2"
DEFAULT_PREFIX="${HOME}/.propagate/bin"
REPO_SLUG="renton4code/propagate-cli"

usage() {
  cat <<'EOF'
Propagate installer

Usage:
  sh install.sh [--version <tag>] [--prefix <dir>] [--insecure]

Options:
  --version   Release tag to install (default: v0.1.0-rc.2)
  --prefix    Install directory for the binary (default: ~/.propagate/bin)
  --insecure  Skip SHA-256 checksum verification (not recommended)
  -h, --help  Show this help text
EOF
}

log() {
  printf '%s\n' "$*"
}

fail() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    fail "required command not found: $1"
  fi
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
    return
  fi
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
    return
  fi
  fail "either sha256sum or shasum is required"
}

normalize_os() {
  case "$(uname -s)" in
    Darwin) printf 'darwin' ;;
    Linux) printf 'linux' ;;
    CYGWIN*|MINGW*|MSYS*) printf 'windows' ;;
    *) fail "unsupported operating system: $(uname -s)" ;;
  esac
}

normalize_arch() {
  case "$(uname -m)" in
    x86_64|amd64) printf 'amd64' ;;
    arm64|aarch64) printf 'arm64' ;;
    *) fail "unsupported architecture: $(uname -m)" ;;
  esac
}

expected_sha_for_v001rc2() {
  case "$1" in
    propagate_0.1.0-rc.2_darwin_amd64.tar.gz) printf '9e824dd8202720336cdd1a4c728955a24282a2f3a1d91f6615e063294dcc4321' ;;
    propagate_0.1.0-rc.2_darwin_arm64.tar.gz) printf '2c781846192e2ce21aa025913451869fbce675b03fe22e206db3a7947ce28a4f' ;;
    propagate_0.1.0-rc.2_linux_amd64.tar.gz) printf 'd9e83e30ebf37155c7bdc19c323d8431121e1206374e61bef8f39066ae6e5f28' ;;
    propagate_0.1.0-rc.2_linux_arm64.tar.gz) printf '915013b07ba071d339c9989c1265b1a40b971aa218690d6d6226a8093dd3e2df' ;;
    propagate_0.1.0-rc.2_windows_amd64.zip) printf '82c700d15bc16deeb8e18dfd7609fab163644762dd989424da411f9de2814070' ;;
    propagate_0.1.0-rc.2_windows_arm64.zip) printf '90bad47714d34da24eeb94a1ed721ffc6071db76c7c005402b6f449053b01526' ;;
    *) return 1 ;;
  esac
}

VERSION="${DEFAULT_VERSION}"
PREFIX="${DEFAULT_PREFIX}"
INSECURE=0

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version)
      [ "$#" -ge 2 ] || fail "--version requires a value"
      VERSION="$2"
      shift 2
      ;;
    --prefix)
      [ "$#" -ge 2 ] || fail "--prefix requires a value"
      PREFIX="$2"
      shift 2
      ;;
    --insecure)
      INSECURE=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

require_cmd curl
require_cmd tar
require_cmd awk

OS="$(normalize_os)"
ARCH="$(normalize_arch)"
VERSION_NO_V="${VERSION#v}"
RELEASE_BASE_URL="https://github.com/${REPO_SLUG}/releases/download/${VERSION}"

if [ "${OS}" = "windows" ]; then
  fail "windows shell install is not supported; download propagate_${VERSION_NO_V}_windows_${ARCH}.zip from ${RELEASE_BASE_URL}"
fi

ASSET_NAME="propagate_${VERSION_NO_V}_${OS}_${ARCH}.tar.gz"
ASSET_URL="${RELEASE_BASE_URL}/${ASSET_NAME}"

TMP_DIR="$(mktemp -d 2>/dev/null || mktemp -d -t propagate-install)"
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

ASSET_PATH="${TMP_DIR}/${ASSET_NAME}"
CHECKSUMS_PATH="${TMP_DIR}/propagate_checksums.txt"

log "Downloading ${ASSET_NAME}..."
curl -fsSL -o "${ASSET_PATH}" "${ASSET_URL}"

if [ "${INSECURE}" -eq 1 ]; then
  log "Skipping checksum verification because --insecure was provided."
else
  EXPECTED_SHA=""
  if [ "${VERSION}" = "v0.1.0-rc.2" ]; then
    EXPECTED_SHA="$(expected_sha_for_v001rc2 "${ASSET_NAME}" || true)"
  fi

  if [ -z "${EXPECTED_SHA}" ]; then
    log "Downloading propagate_checksums.txt for ${VERSION}..."
    curl -fsSL -o "${CHECKSUMS_PATH}" "${RELEASE_BASE_URL}/propagate_checksums.txt"
    EXPECTED_SHA="$(awk -v name="${ASSET_NAME}" '$2 == name { print $1 }' "${CHECKSUMS_PATH}")"
    [ -n "${EXPECTED_SHA}" ] || fail "checksum for ${ASSET_NAME} not found in propagate_checksums.txt"
  fi

  ACTUAL_SHA="$(sha256_file "${ASSET_PATH}")"
  if [ "${ACTUAL_SHA}" != "${EXPECTED_SHA}" ]; then
    fail "checksum mismatch for ${ASSET_NAME}; expected ${EXPECTED_SHA}, got ${ACTUAL_SHA}"
  fi
  log "Checksum verified (${ACTUAL_SHA})."
fi

tar -xzf "${ASSET_PATH}" -C "${TMP_DIR}"

BINARY_PATH=""
for candidate in "${TMP_DIR}/propagate" "${TMP_DIR}"/*/propagate; do
  if [ -f "${candidate}" ]; then
    BINARY_PATH="${candidate}"
    break
  fi
done

[ -n "${BINARY_PATH}" ] || fail "could not find propagate binary in archive"

mkdir -p "${PREFIX}"
if command -v install >/dev/null 2>&1; then
  install -m 755 "${BINARY_PATH}" "${PREFIX}/propagate"
else
  cp "${BINARY_PATH}" "${PREFIX}/propagate"
  chmod 755 "${PREFIX}/propagate"
fi

if [ "${OS}" = "darwin" ] && command -v xattr >/dev/null 2>&1; then
  if xattr -dr com.apple.quarantine "${PREFIX}/propagate" 2>/dev/null; then
    log "Cleared macOS quarantine attribute."
  fi
fi

log "Installed propagate to ${PREFIX}/propagate"
"${PREFIX}/propagate" version || true

case ":${PATH}:" in
  *":${PREFIX}:"*) ;;
  *)
    log ""
    log "Add this directory to your PATH if needed:"
    log "  export PATH=\"${PREFIX}:\$PATH\""
    ;;
esac
