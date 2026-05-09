#!/data/data/com.termux/files/usr/bin/env bash
# termux-install.sh — phone-side BeaconGate installer for Android/Termux.
#
# This script collapses the friend's onboarding from ~6 manual Termux
# commands into one paste. It's the target of:
#
#   curl -fsSL https://raw.githubusercontent.com/trustwall1337/beacongate/master/scripts/termux-install.sh \
#       | bash -s -- --import "bg://config?d=..."
#
# The operator generates that whole command (including the bg:// link)
# with `beacongate-admin export-link --install-qr-png /tmp/qr.png` and
# shares the PNG over Signal etc. The friend scans the QR, copies the
# command, pastes into Termux, and the rest of this script runs.
#
# What the script does:
#   1. Sanity-checks Termux env.
#   2. pkg installs curl, unzip, jq, termux-api.
#   3. termux-setup-storage (one-time; user gets an Android system prompt).
#   4. Resolves the latest release tag from the GitHub API.
#   5. Downloads BeaconGate-<tag>-android-arm64.tar.gz and extracts the
#      client binary into ~/.beacongate/.
#   6. If --import was given, decodes the bg:// link into client_config.json.
#   7. termux-wake-lock so Android's battery optimizer doesn't kill us.
#   8. Starts the client in the background.
#   9. Waits for preflight, prints next-step instructions for NekoBox.
#
# Failure modes are handled with friendly messages, not silent exits —
# someone who doesn't know what BeaconGate is will be reading this.

set -euo pipefail

# --------- defaults / config ---------
INSTALL_DIR="$HOME/.beacongate"
BINARY_NAME="beacongate-client-android-arm64"
BINARY_PATH="$INSTALL_DIR/$BINARY_NAME"
CONFIG_PATH="$INSTALL_DIR/client_config.json"
LOG_PATH="$INSTALL_DIR/client.log"
CONTROL_ADDR="127.0.0.1:9091"
SOCKS_ADDR="127.0.0.1:1080"

REPO="${BG_REPO:-trustwall1337/beacongate}"
RELEASE_TAG="${BG_RELEASE:-latest}"

# --------- arg parsing ---------
usage() {
    cat <<'USAGE'
termux-install.sh: BeaconGate end-to-end Termux installer.

Usage:
  termux-install.sh [--import bg://config?d=...] [--release vX.Y.Z]

Options:
  --import URL    Apply a bg:// share-link (the operator gives you this
                  via QR or copy-paste). The link contains the AES key
                  and Apps Script Deployment ID.
  --release TAG   Pin to a specific GitHub Release tag (default: latest).
  -h, --help      This message.

Environment variables (override defaults):
  BG_REPO         GitHub owner/repo (default: trustwall1337/beacongate)
  BG_RELEASE      Release tag (overridden by --release)

Most users will receive a single-paste command from their operator that
already has all flags filled in. You don't need to invoke this manually.
USAGE
}

IMPORT_LINK=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --import)  IMPORT_LINK="$2"; shift 2 ;;
        --release) RELEASE_TAG="$2"; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) echo "ERROR: unknown arg: $1" >&2; usage; exit 2 ;;
    esac
done

# --------- helpers ---------
say() { printf '\033[0;36m==> %s\033[0m\n' "$*"; }
warn() { printf '\033[0;33mWARN: %s\033[0m\n' "$*" >&2; }
die() { printf '\033[0;31mERROR: %s\033[0m\n' "$*" >&2; exit 1; }

# --------- 1. sanity-check Termux env ---------
if [[ ! -d "/data/data/com.termux/files/usr" ]]; then
    die "This script must be run inside Termux on Android. (/data/data/com.termux/files/usr not found.)
        Termux is on F-Droid: https://f-droid.org/packages/com.termux/
        Do NOT use the Play Store version — it's outdated and does not work."
fi

# --------- 2. install Termux deps ---------
say "installing Termux deps (curl, unzip, jq, termux-api)..."
pkg update -y >/dev/null 2>&1 || true
pkg install -y curl unzip jq termux-api >/dev/null 2>&1 || \
    die "pkg install failed. Make sure Termux has network access and try again."

# --------- 3. storage permission (one-time) ---------
if [[ ! -L "$HOME/storage" ]]; then
    say "requesting storage permission (Android system prompt may appear)..."
    termux-setup-storage 2>/dev/null || warn "termux-setup-storage failed; continuing."
fi

# --------- 4. install dir ---------
mkdir -p "$INSTALL_DIR"
chmod 700 "$INSTALL_DIR"
cd "$INSTALL_DIR"

# --------- 5. resolve release tag ---------
if [[ "$RELEASE_TAG" == "latest" ]]; then
    say "resolving latest release tag from github.com/$REPO ..."
    RELEASE_TAG=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
                    2>/dev/null | jq -r .tag_name) || true
    if [[ -z "$RELEASE_TAG" || "$RELEASE_TAG" == "null" ]]; then
        die "could not resolve a 'latest' release for github.com/$REPO.
        The repo may not have any published releases yet. Ask your
        operator for an explicit --release vX.Y.Z to pin to."
    fi
fi
say "using release: $RELEASE_TAG"

# --------- 6. download + extract binary ---------
ARCHIVE="BeaconGate-${RELEASE_TAG}-android-arm64.tar.gz"
URL="https://github.com/${REPO}/releases/download/${RELEASE_TAG}/${ARCHIVE}"

say "downloading $ARCHIVE ..."
if ! curl -fsSL --retry 3 --retry-delay 2 -o "$ARCHIVE" "$URL"; then
    die "download failed: $URL
        Check that release $RELEASE_TAG exists and has the
        BeaconGate-<tag>-android-arm64 archive uploaded as an asset."
fi

say "extracting client binary ..."
# tarballs from the release pipeline lay everything flat at the archive
# root; -C "$INSTALL_DIR" + extract just the binary keeps the install
# dir clean.
tar -C "$INSTALL_DIR" -xzf "$ARCHIVE" "$BINARY_NAME" 2>/dev/null \
    || tar -C "$INSTALL_DIR" -xzf "$ARCHIVE"
chmod +x "$BINARY_PATH"
rm -f "$ARCHIVE"

# --------- 7. apply --import if given ---------
if [[ -n "$IMPORT_LINK" ]]; then
    say "applying bg:// share-link from operator ..."
    # -import-force overrides any pre-existing config without prompting.
    "$BINARY_PATH" -import "$IMPORT_LINK" -import-force -config "$CONFIG_PATH" \
        || die "applying bg:// link failed. The link may be malformed or
                truncated — ask your operator to re-issue it."
fi

# --------- 8. config sanity check ---------
if [[ ! -f "$CONFIG_PATH" ]]; then
    cat <<EOF >&2

==========================================================
  WARNING: BeaconGate is installed but has no config yet.
==========================================================

  • If you have a bg:// share-link from your operator, re-run with:
      bash <(curl -fsSL https://raw.githubusercontent.com/${REPO}/master/scripts/termux-install.sh) --import "bg://config?d=..."

  • Or copy a client_config.json into ${CONFIG_PATH} manually.

The client cannot start without a config. Exiting.
EOF
    exit 0
fi

say "validating config ..."
"$BINARY_PATH" -config "$CONFIG_PATH" -validate-only >/dev/null \
    || die "config validation failed. Ask your operator to re-issue the bg:// link."

# --------- 9. wake-lock ---------
termux-wake-lock 2>/dev/null \
    || warn "termux-wake-lock not available — Android may kill the client when the screen is off.
            Make sure termux-api is installed (pkg install termux-api) and
            grant it permissions in Android Settings."

# --------- 10. kill any old instance ---------
if pgrep -f "$BINARY_NAME" >/dev/null 2>&1; then
    say "stopping previous client instance ..."
    pkill -f "$BINARY_NAME" || true
    sleep 1
fi

# --------- 11. start client in background ---------
say "starting client in background ..."
nohup "$BINARY_PATH" -config "$CONFIG_PATH" -control-addr "$CONTROL_ADDR" \
    > "$LOG_PATH" 2>&1 &

# --------- 12. wait for preflight (~10s) ---------
say "waiting for preflight ..."
ready=0
for i in $(seq 1 20); do
    if curl -fsS -m 2 "http://${CONTROL_ADDR}/api/status" >/dev/null 2>&1; then
        ready=1; break
    fi
    sleep 1
done

# --------- 13. print final status + next steps ---------
if [[ $ready -eq 1 ]]; then
    state=$(curl -fsS "http://${CONTROL_ADDR}/api/status" | jq -r '.state')
    cat <<EOF

==========================================================
  ✅ BeaconGate is running on this phone.
==========================================================

  Client state:   $state
  SOCKS5 server:  $SOCKS_ADDR
  Control API:    http://$CONTROL_ADDR/api/status
  Log file:       $LOG_PATH

NEXT STEPS:

  1. Install NekoBox (or v2rayNG) from F-Droid.
     https://f-droid.org/packages/io.nekohasekai.sfa/

  2. In NekoBox: Profiles → New → SOCKS5
       Server: 127.0.0.1
       Port:   1080

  3. Tap Connect.

  Apps that route through NekoBox now flow through the BeaconGate
  tunnel. Confirm with this curl from Termux:
     curl -x socks5h://${SOCKS_ADDR} https://api.ipify.org

  It should print your operator's VPS public IP (NOT your phone's
  mobile-data IP). If it does, the tunnel is live.

If anything stops working:
  • Check status:   curl http://$CONTROL_ADDR/api/status
  • Tail log:       tail -f $LOG_PATH
  • Restart:        bash <(curl -fsSL https://raw.githubusercontent.com/${REPO}/master/scripts/termux-install.sh) --import "bg://..."
                    (re-run with the same bg:// link your operator gave you)

EOF
else
    cat <<EOF >&2

==========================================================
  ⚠ Client started but didn't reach a healthy state.
==========================================================

  Check the log at $LOG_PATH for clues:
    tail -50 $LOG_PATH

  Common causes:
    • Apps Script forwarder URL unreachable from this network.
    • The operator's VPS is offline.
    • This phone has no working internet connection.

  Re-run the install command after you've fixed the underlying
  issue, or ask your operator to verify their server is healthy.

EOF
    exit 1
fi
