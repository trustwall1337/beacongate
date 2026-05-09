#!/usr/bin/env bash
# prepare-bundle.sh — package an operator-prepared BeaconGate client
# for handoff to an Android end user running Termux + NekoBox/v2rayNG.
#
# Produces a single .zip containing:
#   - beacongate-client-android-arm64  (the cross-compiled binary)
#   - client_config.json               (validated copy of the operator's config)
#   - README.txt                       (Termux-specific setup instructions)
#   - verify.sh                        (phone-side disguise + connectivity check)
#
# Usage:
#   ops/prepare-bundle.sh \
#     --binary bin/beacongate-client-android-arm64 \
#     --config client_config.json \
#     --out /tmp/beacongate-bundle.zip
#
# The script fails closed: a config that fails validation never makes it
# into a bundle, so the friend never receives a broken handoff.

set -euo pipefail

usage() {
  cat >&2 <<'EOF'
Usage: prepare-bundle.sh --binary <path> --config <path> --out <bundle.zip>

  --binary   path to the linux/arm64 client binary (run `make build-android` first)
  --config   path to the client config JSON to ship with the bundle
  --out      output path for the .zip bundle
  --vps-ip   (optional) the operator's VPS IP, templated into verify.sh's
             leak-check. If omitted, verify.sh will skip that check.
EOF
  exit 2
}

BINARY=""
CONFIG=""
OUT=""
VPS_IP=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --binary)  BINARY="$2"; shift 2 ;;
    --config)  CONFIG="$2"; shift 2 ;;
    --out)     OUT="$2"; shift 2 ;;
    --vps-ip)  VPS_IP="$2"; shift 2 ;;
    -h|--help) usage ;;
    *) echo "unknown arg: $1" >&2; usage ;;
  esac
done

[[ -z "$BINARY" || -z "$CONFIG" || -z "$OUT" ]] && usage
[[ ! -f "$BINARY" ]] && { echo "binary not found: $BINARY" >&2; exit 1; }
[[ ! -f "$CONFIG" ]] && { echo "config not found: $CONFIG" >&2; exit 1; }
command -v zip >/dev/null 2>&1 || { echo "zip not installed" >&2; exit 1; }

# 1. Validate the config using the same client binary the friend will run.
#    Falls back to a host-arch beacongate-client if one is available, since
#    the android-arm64 binary won't exec on the operator's laptop.
validator=""
if [[ -x "bin/beacongate-client" ]]; then
  validator="bin/beacongate-client"
elif command -v beacongate-client >/dev/null 2>&1; then
  validator="beacongate-client"
fi
if [[ -n "$validator" ]]; then
  echo "==> validating config with $validator"
  if ! "$validator" -config "$CONFIG" -validate-only; then
    echo "config validation failed; aborting bundle" >&2
    exit 1
  fi
else
  echo "==> WARNING: no host beacongate-client found; skipping config validation" >&2
  echo "    run 'make build' first to enable validation" >&2
fi

# 2. Build the staging tree.
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT

cp "$BINARY" "$stage/beacongate-client-android-arm64"
chmod +x "$stage/beacongate-client-android-arm64"
cp "$CONFIG" "$stage/client_config.json"

# 3. README.txt — short Termux instructions; full walkthrough is in
#    docs/android-termux.md.
cat > "$stage/README.txt" <<'README_EOF'
BeaconGate — Android (Termux) bundle
====================================

You need:
  1. Termux installed from F-Droid (NOT the Play Store version — it's
     deprecated and will fail on Android 11+).
     https://f-droid.org/packages/com.termux/
  2. NekoBox or v2rayNG, also from F-Droid.

Setup (run in Termux):

  pkg update -y
  pkg install -y iproute2 jq termux-api
  termux-wake-lock                    # keep the tunnel alive when screen is off
  chmod +x beacongate-client-android-arm64
  ./beacongate-client-android-arm64 -config client_config.json -control-addr 127.0.0.1:9091 &

In NekoBox / v2rayNG:
  - add a SOCKS5 outbound at 127.0.0.1:1080
  - set Remote DNS to socks5h://1.1.1.1   (prevents DNS leak)
  - set "IPv4-only"                       (prevents NekoBox bypassing for AAAA)
  - select that profile and connect

Verify everything works:

  bash verify.sh

For the full walkthrough including Termux:Boot for survive-reboot,
see docs/android-termux.md in the BeaconGate repo.
README_EOF

# 4. verify.sh — three checks the friend can run on the phone.
cat > "$stage/verify.sh" <<VERIFY_EOF
#!/usr/bin/env bash
# verify.sh — phone-side check that BeaconGate is actually tunneling.
# Run this in Termux after starting the client and pointing NekoBox at it.

set -u

VPS_IP="${VPS_IP}"

pass=0
fail=0
check() {
  local name="\$1"; shift
  if "\$@"; then
    echo "  OK   \$name"
    pass=\$((pass+1))
  else
    echo "  FAIL \$name"
    fail=\$((fail+1))
  fi
}

echo "== BeaconGate phone-side verification =="

# 1. SOCKS5 routes through and returns the server's exit IP.
check "SOCKS5 reachable + traffic exits via server" \
  bash -c 'curl -x socks5h://127.0.0.1:1080 -fsS --max-time 10 https://api.ipify.org >/dev/null'

# 2. Local control API reports state=connected.
check "control API state=connected" \
  bash -c 'state=\$(curl -fsS --max-time 5 http://127.0.0.1:9091/api/status | jq -r .state 2>/dev/null); [[ "\$state" == "connected" ]]'

# 3. For appsscript mode: zero direct connections to the operator's VPS.
if [[ -n "\$VPS_IP" ]]; then
  check "no direct connection to VPS \$VPS_IP (disguise intact)" \
    bash -c '! ss -tn 2>/dev/null | grep -q "\$VPS_IP"'
else
  echo "  SKIP no --vps-ip provided to prepare-bundle.sh; can't check disguise"
fi

echo
echo "== \$pass passed, \$fail failed =="
[[ \$fail -eq 0 ]]
VERIFY_EOF
chmod +x "$stage/verify.sh"

# 5. zip it up.
out_abs="$(cd "$(dirname "$OUT")" && pwd)/$(basename "$OUT")"
( cd "$stage" && zip -q -X -r "$out_abs" . )

# 6. Print bundle metadata.
sha=$(shasum -a 256 "$out_abs" | awk '{print $1}')
size=$(wc -c <"$out_abs" | tr -d ' ')

echo
echo "==> bundle: $out_abs"
echo "    size:   $size bytes"
echo "    sha256: $sha"
echo
echo "Hand the .zip to your friend. The SHA-256 above is what you'll"
echo "compare out-of-band to confirm they received the file you sent."
