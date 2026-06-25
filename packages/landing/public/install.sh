#!/bin/sh
set -eu

DEFAULT_VERSION="v0.1.0-rc.1"
DEFAULT_PREFIX="${HOME}/.propagate/bin"
REPO_SLUG="renton4code/propagate-cli"

usage() {
  cat <<'EOF'
Propagate installer

Usage:
  sh install.sh [--version <tag>] [--prefix <dir>] [--insecure]

Options:
  --version   Release tag to install (default: v0.1.0-rc.1)
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

expected_sha_for_v001rc1() {
  case "$1" in
    propagate_0.1.0-rc.1_darwin_amd64.tar.gz) printf '4915f4ba33089d0980d14657ba92a488c8f03c7846209eb578f809c136b155e9' ;;
    propagate_0.1.0-rc.1_darwin_arm64.tar.gz) printf '5d96b06ada261ff6c383567111bbc87c7a3593869d64a09ce3f1f6bc7e1cc8b3' ;;
    propagate_0.1.0-rc.1_linux_amd64.tar.gz) printf '5ace4e5f4290d8c97d9a99738485ea5fcde8c6224869e9ffa09868a45ff335f4' ;;
    propagate_0.1.0-rc.1_linux_arm64.tar.gz) printf '1c9a9e9334c258b5306870a1449ea6a91df0deb14b858b1c4698e267249ee23a' ;;
    propagate_0.1.0-rc.1_windows_amd64.zip) printf '5b52a7f76f7e30b795336d498732bcb1f17c14ec1089c5aecd8e2d56f7634efb' ;;
    propagate_0.1.0-rc.1_windows_arm64.zip) printf 'fa4b6691e2a7ab7883b4a3d15df2bf00cb9da6e2185cedd10b8311ecb5205d70' ;;
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
  if [ "${VERSION}" = "v0.1.0-rc.1" ]; then
    EXPECTED_SHA="$(expected_sha_for_v001rc1 "${ASSET_NAME}" || true)"
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
