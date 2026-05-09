#!/usr/bin/env bash
# install.sh — BeaconGate VPS one-liner installer.
#
#   curl -fsSL https://raw.githubusercontent.com/trustwall1337/beacongate/main/scripts/install.sh | bash
#
# What this does:
#   1. Detects OS / arch.
#   2. Downloads the latest GitHub Release tarball + cosign signature.
#   3. Bootstraps cosign if not present.
#   4. Verifies the cosign signature against the BeaconGate workflow's
#      OIDC identity. Aborts on any mismatch.
#   5. Verifies the SHA-256 of the tarball.
#   6. Extracts binaries to /usr/local/bin.
#   7. Generates an AES key, writes /etc/beacongate/server_config.json
#      (mode 0600, owned by the beacongate user).
#   8. Installs the systemd unit and enables the service.
#
# Trust model:
#   You are trusting GitHub OIDC + sigstore + this repo's release
#   pipeline to produce the binary. The cosign verification step
#   binds the artifact to the specific GitHub Actions workflow
#   identity at .github/workflows/release.yml in this repo. If you
#   cannot extend that trust, do NOT run this script — manually
#   download from GitHub Releases, run the cosign verify-blob
#   command from the release notes, and install yourself.
#
# Idempotency:
#   Re-running this script is safe. If /etc/beacongate/server_config.json
#   already exists, the existing key is preserved and the script skips
#   key generation. Binaries are always overwritten with the latest
#   release version.

set -euo pipefail

# ---------- configuration --------------------------------------------------

REPO="trustwall1337/beacongate"
INSTALL_BIN_DIR="${INSTALL_BIN_DIR:-/usr/local/bin}"
CONFIG_DIR="${CONFIG_DIR:-/etc/beacongate}"
DATA_DIR="${DATA_DIR:-/var/lib/beacongate}"
SYSTEMD_UNIT="${SYSTEMD_UNIT:-/etc/systemd/system/beacongate-server.service}"
SVC_USER="${SVC_USER:-beacongate}"
LISTEN_ADDR="${LISTEN_ADDR:-:8080}"

# Cosign identity binding. Update if the repo moves/renames.
COSIGN_IDENTITY_REGEXP="https://github.com/${REPO}/.+"
COSIGN_OIDC_ISSUER="https://token.actions.githubusercontent.com"

# ---------- helpers --------------------------------------------------------

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*" >&2; }
warn() { printf '\033[1;33m==>\033[0m %s\n' "$*" >&2; }
err()  { printf '\033[1;31m==>\033[0m %s\n' "$*" >&2; exit 1; }

require_root() {
  if [ "$(id -u)" -ne 0 ]; then
    err "This script needs to be run as root (try: sudo bash install.sh)."
  fi
}

detect_arch() {
  local uname_m
  uname_m="$(uname -m)"
  case "$uname_m" in
    x86_64|amd64) echo "amd64" ;;
    aarch64|arm64) echo "arm64" ;;
    *) err "Unsupported CPU architecture: $uname_m" ;;
  esac
}

detect_os() {
  local uname_s
  uname_s="$(uname -s)"
  case "$uname_s" in
    Linux) echo "linux" ;;
    Darwin) err "This installer is for Linux servers only. On macOS, download the release tarball manually and follow docs/deployment.md." ;;
    *) err "Unsupported OS: $uname_s. This installer targets Linux." ;;
  esac
}

# get_latest_tag fetches the latest non-prerelease tag from GitHub.
get_latest_tag() {
  local api="https://api.github.com/repos/${REPO}/releases/latest"
  local tag
  tag="$(curl -fsSL "$api" 2>/dev/null | sed -nE 's/.*"tag_name":[[:space:]]*"([^"]+)".*/\1/p' | head -1)"
  if [ -z "$tag" ]; then
    err "Could not fetch latest release tag from $api. Check network connectivity and that the repository has at least one release."
  fi
  echo "$tag"
}

# bootstrap_cosign installs cosign if it isn't already on PATH.
# We pin to a specific version so a sudden cosign-side breakage
# doesn't break installs.
bootstrap_cosign() {
  if command -v cosign >/dev/null 2>&1; then
    log "cosign already installed: $(cosign version --json 2>/dev/null | sed -nE 's/.*"GitVersion":"([^"]+)".*/\1/p' || echo unknown)"
    return 0
  fi
  log "cosign not found; bootstrapping..."
  local cosign_ver="v2.4.1"
  local arch
  arch="$(detect_arch)"
  local url="https://github.com/sigstore/cosign/releases/download/${cosign_ver}/cosign-linux-${arch}"
  curl -fsSL "$url" -o /usr/local/bin/cosign
  chmod +x /usr/local/bin/cosign
  log "cosign ${cosign_ver} installed at /usr/local/bin/cosign"
}

# ---------- main -----------------------------------------------------------

require_root

OS="$(detect_os)"
ARCH="$(detect_arch)"
log "Detected: ${OS}/${ARCH}"

bootstrap_cosign

TAG="${BEACONGATE_TAG:-$(get_latest_tag)}"
log "Installing BeaconGate ${TAG}"

ARCHIVE_BASE="BeaconGate-${TAG}-${OS}-${ARCH}"
ARCHIVE_NAME="${ARCHIVE_BASE}.tar.gz"
CHECKSUMS_NAME="BeaconGate-${TAG}-checksums.txt"

DOWNLOAD_BASE="https://github.com/${REPO}/releases/download/${TAG}"
TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

cd "$TMPDIR"

log "Downloading release artifacts..."
curl -fsSL "${DOWNLOAD_BASE}/${ARCHIVE_NAME}"          -o "${ARCHIVE_NAME}"
curl -fsSL "${DOWNLOAD_BASE}/${CHECKSUMS_NAME}"        -o "${CHECKSUMS_NAME}"
curl -fsSL "${DOWNLOAD_BASE}/${CHECKSUMS_NAME}.sig"    -o "${CHECKSUMS_NAME}.sig"
curl -fsSL "${DOWNLOAD_BASE}/${CHECKSUMS_NAME}.cert"   -o "${CHECKSUMS_NAME}.cert"

log "Verifying cosign signature on ${CHECKSUMS_NAME}..."
if ! cosign verify-blob \
      --certificate "${CHECKSUMS_NAME}.cert" \
      --signature   "${CHECKSUMS_NAME}.sig" \
      --certificate-identity-regexp "${COSIGN_IDENTITY_REGEXP}" \
      --certificate-oidc-issuer     "${COSIGN_OIDC_ISSUER}" \
      "${CHECKSUMS_NAME}"; then
  err "cosign verification FAILED. Refusing to install. See docs/troubleshooting.md \"cosign verify-blob fails\"."
fi
log "Signature OK — checksums.txt was signed by ${REPO}'s release workflow."

log "Verifying SHA-256 of ${ARCHIVE_NAME}..."
if ! sha256sum --check --ignore-missing "${CHECKSUMS_NAME}" 2>&1 | grep -qE "^${ARCHIVE_NAME}: OK"; then
  err "SHA-256 mismatch on ${ARCHIVE_NAME}. Refusing to install."
fi
log "SHA-256 OK."

log "Extracting..."
mkdir -p stage
tar -xzf "${ARCHIVE_NAME}" -C stage

log "Installing binaries to ${INSTALL_BIN_DIR}..."
install -m 0755 stage/beacongate-client "${INSTALL_BIN_DIR}/beacongate-client"
install -m 0755 stage/beacongate-server "${INSTALL_BIN_DIR}/beacongate-server"
install -m 0755 stage/beacongate-admin  "${INSTALL_BIN_DIR}/beacongate-admin"

log "Setting up service user / directories..."
if ! id "${SVC_USER}" >/dev/null 2>&1; then
  useradd --system --home "${DATA_DIR}" --shell /usr/sbin/nologin "${SVC_USER}"
fi
install -d -m 0750 -o "${SVC_USER}" -g "${SVC_USER}" "${DATA_DIR}"
install -d -m 0750 "${CONFIG_DIR}"

if [ -f "${CONFIG_DIR}/server_config.json" ]; then
  log "Existing ${CONFIG_DIR}/server_config.json found — preserving existing key (idempotent re-run)."
else
  log "Generating fresh AES-256 key..."
  KEY="$("${INSTALL_BIN_DIR}/beacongate-admin" gen-key)"
  cat > "${CONFIG_DIR}/server_config.json" <<JSON
{
  "server_id": "$(hostname -s)",
  "listen_addr": "${LISTEN_ADDR}",
  "tunnel_path": "/tunnel",
  "health_path": "/healthz",
  "key": "${KEY}",
  "policy": {
    "baseline_enabled": true,
    "store_path": "${DATA_DIR}/policy.json"
  },
  "admin": {
    "enabled": true,
    "listen_addr": "127.0.0.1:9090",
    "token": ""
  }
}
JSON
  chmod 0640 "${CONFIG_DIR}/server_config.json"
  chown "${SVC_USER}:${SVC_USER}" "${CONFIG_DIR}/server_config.json"
fi

log "Installing systemd unit..."
cat > "${SYSTEMD_UNIT}" <<UNIT
[Unit]
Description=BeaconGate relay server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${SVC_USER}
Group=${SVC_USER}
ExecStart=${INSTALL_BIN_DIR}/beacongate-server -config ${CONFIG_DIR}/server_config.json
Restart=on-failure
RestartSec=5s
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
NoNewPrivileges=true
ReadWritePaths=${DATA_DIR}

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable --now beacongate-server

log "Service status:"
systemctl --no-pager --full status beacongate-server | head -10 || true

KEY_ECHO="$(grep -E '"key"' "${CONFIG_DIR}/server_config.json" | sed -E 's/.*"key": "([^"]+)".*/\1/')"

cat <<DONE

==============================================================
BeaconGate ${TAG} is installed and running.

Server config: ${CONFIG_DIR}/server_config.json
Logs:          journalctl -u beacongate-server -f
Health check:  curl -fsS http://127.0.0.1:8080/healthz

To configure a client, copy this AES key into your client_config.json:

${KEY_ECHO}

(Save it somewhere safe — anyone with this key can use your tunnel.)

Next steps:
- For 'appsscript' transport: deploy apps_script/Code.gs in your Google
  account, point the Apps Script's RELAY_URL at this VPS, and use the
  Deployment ID in client_config.appsscript.example.json.
- For 'https' transport: front this server with TLS (Caddy/nginx) on
  a public hostname; see docs/deployment.md Playbook A.
==============================================================
DONE
