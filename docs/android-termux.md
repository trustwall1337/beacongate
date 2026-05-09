# BeaconGate on Android (Termux + NekoBox/v2rayNG)

This is the end-user setup guide for running BeaconGate on an Android
phone. Phase 1 deliberately does not ship a native APK; instead, the
operator hands the user a prepared bundle and the user runs the
ordinary Linux client binary inside [Termux](https://termux.dev). An
Android SOCKS5 client (NekoBox or v2rayNG) routes phone-app traffic
through the local listener.

If you are the operator preparing the bundle, see
[operator-handoff-checklist.md](operator-handoff-checklist.md). If you
are the friend the operator handed the bundle to, **start here**.

---

## Prerequisites

You need two apps from **F-Droid**, not the Play Store:

1. **Termux** — <https://f-droid.org/packages/com.termux/>
   The Play Store version was deprecated in 2020 and **fails on
   Android 11 and newer**. Install only the F-Droid build.
2. **NekoBox for Android** (recommended) — <https://f-droid.org/packages/moe.nb4a/>
   Or **v2rayNG** if you already use it.

Optional but useful for the "survive a reboot" setup:

- **Termux:Boot** from F-Droid — <https://f-droid.org/packages/com.termux.boot/>

You also need the **bundle** (a `.zip` file) the operator sent you. It
contains:

- `beacongate-client-android-arm64` — the BeaconGate binary
- `client_config.json` — your config, prepared by the operator
- `README.txt` — a short version of these instructions
- `verify.sh` — a phone-side check script

---

## Step 1 — Get the bundle onto the phone

Pick whichever is easiest:

- **Messenger / Email:** Save the `.zip` to your `Download` folder.
- **`scp` from a laptop** (if you've installed `openssh` in Termux):
  ```sh
  pkg install openssh
  sshd
  # then from the laptop: scp bundle.zip <phone-ip>:/sdcard/Download/
  ```
- **Files app drop:** Save to any folder; you'll move it next.

Then in Termux:

```sh
termux-setup-storage   # one-time: lets Termux read ~/storage/downloads
cp ~/storage/downloads/bundle.zip ~
cd ~ && unzip bundle.zip && cd <bundle-folder>
```

---

## Step 2 — Install the helpers Termux needs

```sh
pkg update -y
pkg install -y iproute2 jq termux-api
```

- `iproute2` — provides `ss` (used by the verify script).
- `jq` — JSON parser used by the verify script.
- `termux-api` — needed for `termux-wake-lock` (Step 4).

---

## Step 3 — Run the client

```sh
chmod +x beacongate-client-android-arm64
./beacongate-client-android-arm64 -config client_config.json -control-addr 127.0.0.1:9091 &
```

You should see a log line like:

```
startup.diagnostics transport_healthy=true probe_ok=true
startup.listening addr=127.0.0.1:1080
control_api.listening addr=127.0.0.1:9091
```

If `transport_healthy=false`, see the
[troubleshooting guide](troubleshooting.md) — usually it's the wrong
config or the operator's server is unreachable from your network.

---

## Step 4 — Keep it running (load-bearing)

**Without this step, the tunnel will pause within minutes whenever
your screen is off.** Android's battery optimizer kills Termux
processes aggressively on Android 12 and newer. You need:

```sh
termux-wake-lock
```

Run this **every time** you start the BeaconGate client. It keeps
Termux running with the screen off. You'll see a persistent
notification that says "Termux is running."

If you also want BeaconGate to survive a phone reboot:

1. Install **Termux:Boot** from F-Droid (separate app).
2. Open it once so Android grants it the autostart permission.
3. Create `~/.termux/boot/start-beacongate.sh`:

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

   Replace `<bundle-folder>` with your actual unpacked path.

---

## Step 5 — Configure NekoBox (or v2rayNG)

In **NekoBox**:

1. Tap the `+` → **Custom Outbound** (or **SOCKS5**).
2. Type: **SOCKS5**, Address: `127.0.0.1`, Port: `1080`.
3. Open **Settings → Route**:
   - Set **Remote DNS** to `socks5h://1.1.1.1`.
     This is critical: if DNS goes through the system resolver instead
     of the SOCKS proxy, your ISP sees every domain you visit and the
     disguise is broken.
   - Set **IPv4 only** (or "Prefer IPv4"). NekoBox will otherwise
     resolve AAAA records over the system path and bypass SOCKS for
     IPv6 destinations.
4. Tap the profile to select it, then the connect button.

For **v2rayNG** the option names differ slightly but the principle is
the same: SOCKS5 outbound at `127.0.0.1:1080`, DNS forced through the
proxy, IPv4-preferred.

---

## Step 6 — Verify

```sh
bash verify.sh
```

You should see something like:

```
== BeaconGate phone-side verification ==
  OK   SOCKS5 reachable + traffic exits via server
  OK   control API state=connected
  OK   no direct connection to VPS 203.0.113.5 (disguise intact)

== 3 passed, 0 failed ==
```

If any check **FAILs**, see [troubleshooting.md](troubleshooting.md).

A second sanity check from the phone's browser: open
<https://dnsleaktest.com> and run the standard test. The DNS resolver
shown should belong to your operator's chosen DNS (e.g. Cloudflare
`1.1.1.1`), **not** your mobile carrier or local ISP. If you see
your ISP, NekoBox's DNS-over-SOCKS toggle is not on.

---

## Stopping it

```sh
# bring the background job to the foreground and Ctrl-C, or:
pkill -f beacongate-client-android-arm64
termux-wake-unlock
```

In NekoBox, tap disconnect.

---

## Common surprises

- **Battery: tunnel pauses after a few minutes screen-off.** You
  forgot `termux-wake-lock`, or Android killed Termux despite the
  wake-lock. On some phones (Xiaomi, Huawei) you also need to
  manually exempt Termux from battery optimization in the system
  Settings.
- **Some apps still leak DNS.** Some Android apps make raw DNS
  queries that bypass system settings. NekoBox's "DNS-over-SOCKS"
  toggle catches most of them; for the rest, NekoBox has a per-app
  "Mode" that forces all queries through the proxy.
- **Captive portals (hotel/airport WiFi).** Your phone won't connect
  through BeaconGate until you've signed into the captive portal in
  the system browser first. Pause BeaconGate, sign in, then resume.
- **Browser shows "ERR_PROXY_CONNECTION_FAILED" briefly.** Normal
  during the first probe. If it persists, `verify.sh` will tell you
  which check is failing.

---

## What this setup does and does not protect

The `appsscript` transport routes your BeaconGate traffic through
Google Apps Script, so a network observer between you and Google sees
ordinary HTTPS to `script.google.com`. That is the censorship-evasion
property of the system.

It does **not** make you anonymous. The operator's server still sees
all of your decrypted-tunnel traffic before it goes to the
destination, and the destination still sees the server's IP. If you
need anonymity, BeaconGate is not the right tool; use Tor.

It also does not defend against:

- Traffic-pattern analysis (a sophisticated observer may still infer
  *that* you're tunneling, even if not *what*).
- TLS-fingerprint analysis.
- Google-side classifiers running on Apps Script itself.
- A future block of `script.google.com` URL paths by an upstream
  censor — BeaconGate raises the cost of blocking but does not make
  blocking impossible.

See the operator's [SECURITY.md](../SECURITY.md) for the full residual
risk model.
