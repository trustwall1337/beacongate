# BeaconGate on Android — Termux (legacy)

> **Use the native Android app instead when possible.** This doc
> describes the original Termux + SOCKS5-client flow that predates the
> APK. The native app ([mobile/android/README.md](../mobile/android/README.md))
> is simpler, survives screen-off reliably without `termux-wake-lock`,
> and ships as one APK with no bundle to unpack. Keep this guide
> around for cases where installing the APK is not an option (sideload
> blocked, MDM, F-Droid-only constraints).

The operator builds a bundle on a laptop and hands it over. The user
runs the ordinary Linux client binary inside [Termux](https://termux.dev)
and points an Android SOCKS5 client (NekoBox or v2rayNG) at the local
listener.

For the operator side of the handoff, see
[operator-handoff-checklist.md](operator-handoff-checklist.md).

---

## Prerequisites

Install both from **F-Droid**, not the Play Store:

1. **Termux** — <https://f-droid.org/packages/com.termux/>
   The Play Store build is deprecated and fails on Android 11+.
2. **NekoBox for Android** (recommended) — <https://f-droid.org/packages/moe.nb4a/>
   Or **v2rayNG** if you already use it.

Optional, for surviving a reboot:

- **Termux:Boot** — <https://f-droid.org/packages/com.termux.boot/>

The operator sends a `.zip` bundle containing:

- `beacongate-client-android-arm64` — the BeaconGate binary
- `client_config.json` — your config
- `README.txt` — a short version of these instructions
- `verify.sh` — a phone-side check script

---

## 1. Get the bundle onto the phone

Pick whichever path is easiest (Signal/email to `Download/`,
`scp` via `pkg install openssh`, or a Files-app drop), then in Termux:

```sh
termux-setup-storage   # one-time: grants Termux read on ~/storage/downloads
cp ~/storage/downloads/bundle.zip ~
cd ~ && unzip bundle.zip && cd <bundle-folder>
```

## 2. Install helpers

```sh
pkg update -y
pkg install -y iproute2 jq termux-api
```

- `iproute2` — `ss` for the verify script.
- `jq` — JSON parsing.
- `termux-api` — provides `termux-wake-lock`.

## 3. Run the client

```sh
chmod +x beacongate-client-android-arm64
./beacongate-client-android-arm64 -config client_config.json -control-addr 127.0.0.1:9091 &
```

Expected:

```
startup.diagnostics transport_healthy=true probe_ok=true
startup.listening addr=127.0.0.1:1080
control_api.listening addr=127.0.0.1:9091
```

If `transport_healthy=false`, see
[troubleshooting.md](troubleshooting.md) — usually it's wrong config
or the server is unreachable from your network.

## 4. Keep it running (required)

Without this step, the tunnel pauses within minutes whenever the
screen is off. Android's battery optimizer kills Termux processes
aggressively on Android 12+.

```sh
termux-wake-lock
```

Run it every time you start the client. A persistent "Termux is
running" notification confirms it.

To survive reboots, install **Termux:Boot** and create
`~/.termux/boot/start-beacongate.sh`:

```sh
mkdir -p ~/.termux/boot
cat > ~/.termux/boot/start-beacongate.sh <<'EOF'
#!/data/data/com.termux/files/usr/bin/bash
termux-wake-lock
cd ~/<bundle-folder>
./beacongate-client-android-arm64 -config client_config.json -control-addr 127.0.0.1:9091
EOF
chmod +x ~/.termux/boot/start-beacongate.sh
```

Replace `<bundle-folder>` with your unpacked path.

## 5. Configure the SOCKS5 client

In **NekoBox**:

1. `+` → **Custom Outbound** → type **SOCKS5**, address `127.0.0.1`,
   port `1080`.
2. **Settings → Route**:
   - **Remote DNS**: `socks5h://1.1.1.1` — critical. If DNS goes via
     the system resolver, your ISP sees every domain and the disguise
     is broken.
   - **IPv4 only** (or *Prefer IPv4*) — otherwise NekoBox resolves
     AAAA records over the system path and bypasses SOCKS for IPv6.
3. Tap the profile to select it, then **Connect**.

For **v2rayNG**: same idea — SOCKS5 outbound at `127.0.0.1:1080`,
DNS forced through the proxy, IPv4-preferred.

## 6. Verify

```sh
bash verify.sh
```

Expected:

```
== BeaconGate phone-side verification ==
  OK   SOCKS5 reachable + traffic exits via server
  OK   control API state=connected
  OK   no direct connection to VPS <ip> (disguise intact)

== 3 passed, 0 failed ==
```

A second sanity check from the phone's browser:
<https://dnsleaktest.com> should show your operator's chosen DNS
(e.g. Cloudflare `1.1.1.1`), not your mobile carrier or ISP. If you
see the carrier, NekoBox's DNS-over-SOCKS toggle is off.

## Stopping

```sh
pkill -f beacongate-client-android-arm64
termux-wake-unlock
```

Then disconnect in NekoBox.

---

## Common surprises

- **Tunnel pauses after a few minutes screen-off.** `termux-wake-lock`
  was missed, or Android killed Termux despite the wake-lock. On some
  phones (Xiaomi, Huawei) you also need to manually exempt Termux
  from battery optimization in the system Settings.
- **Some apps still leak DNS.** A few Android apps make raw DNS
  queries that bypass system settings. NekoBox's *DNS-over-SOCKS*
  toggle catches most; for the rest, NekoBox's per-app *Mode* forces
  all queries through the proxy.
- **Captive portals.** Phone won't connect through BeaconGate until
  you sign into the captive portal in the system browser. Pause
  BeaconGate, sign in, then resume.
- **Browser shows `ERR_PROXY_CONNECTION_FAILED` briefly.** Normal
  during the first probe. If it persists, `verify.sh` tells you which
  check is failing.

See [SECURITY.md](../SECURITY.md) for the residual-risk model that
applies to either Android path.
