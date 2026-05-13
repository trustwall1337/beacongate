# Getting started

End-to-end setup for an operator new to BeaconGate: rent a VPS, build
and run the server, deploy the Apps Script forwarder, generate a
per-user config, and install the native Android client on a phone.

For a terser config-shape reference once you know the moving parts,
read [deployment.md](deployment.md) instead.

Conventions:

- **OPERATOR** = you, running this guide. You own the VPS and the
  Google account hosting Apps Script.
- **END USER** = the person whose phone runs the Android app.
- Commands tagged `mac%` run on your Mac/Linux laptop.
- Commands tagged `vps#` run on the VPS as root, `vps$` as a regular
  user.
- Replace placeholders `YOUR.VPS.IP`, `DEPLOYMENT_ID`, `BASE64_KEY`
  with your real values.

---

## Prerequisites

| Resource | Required |
| --- | --- |
| Laptop | macOS / Linux / WSL with `ssh`, `scp`, `git`, a text editor, and `adb` (`brew install --cask android-platform-tools`). Docker for the Android APK build. |
| VPS | Any cloud provider, Ubuntu 24.04 LTS, ≥1 vCPU, ≥1 GB RAM, located outside the censored network. Example below uses Hetzner CX23 (€4.75/month). |
| Google account | A **personal** Google account (not a work account) to host the Apps Script forwarder. Google can suspend a script that trips abuse heuristics; don't bet a work identity on it. |
| Phone | Android 8+. iOS is not supported today. |

You do **not** need Go installed on the laptop — the server and Linux
client are built on the VPS, and the Android APK is built inside a
Docker image that supplies its own toolchain.

---

## 1. Provision the VPS

The walkthrough uses Hetzner; any provider works identically once you
have an IP and SSH access.

1. Create a Hetzner project at <https://console.hetzner.com/>.
2. **Before creating the server**, add your SSH public key to
   *Project → Security → SSH Keys*. If your existing key was minted
   for a work email, generate a fresh one for personal infra:
   ```sh
   mac% ssh-keygen -t ed25519 -C "your-personal@example.com" \
        -f ~/.ssh/id_ed25519_personal
   ```
   The comment field of a public key is plaintext in
   `authorized_keys`; pick one that won't leak a work identity.
3. *Create Server*:
   - **Image:** Ubuntu 24.04
   - **Type:** CX23 (or larger for many concurrent users)
   - **SSH Keys:** select your personal key
   - **Name:** `beacongate-server`
4. Copy the IPv4 address Hetzner shows — that's `YOUR.VPS.IP`.

If you forgot to attach the SSH key, reset the root password via the
Hetzner console, SSH in once, and append your public key to
`/root/.ssh/authorized_keys` manually.

Add a convenience alias on the laptop:

```
# ~/.ssh/config
Host beacongate-vps
    HostName YOUR.VPS.IP
    User root
    IdentityFile ~/.ssh/id_ed25519_personal
    IdentitiesOnly yes
```

```sh
mac% ssh beacongate-vps hostname   # confirms key-auth works
```

---

## 2. Install dependencies and Go on the VPS

```sh
vps# apt-get update
vps# apt-get install -y git jq zip unzip ca-certificates curl
```

Ubuntu's apt-shipped Go is older than the repo's `go.mod` requires.
Install from the official tarball:

```sh
vps# cd /tmp
vps# GO_VER=1.25.10
vps# curl -fsSLO https://go.dev/dl/go${GO_VER}.linux-amd64.tar.gz

# Verify SHA-256 against go.dev's manifest:
vps# EXPECTED=$(curl -fsSL 'https://go.dev/dl/?mode=json&include=all' | \
    jq -r '.[] | select(.version=="go'${GO_VER}'") | .files[] |
           select(.os=="linux" and .arch=="amd64" and .kind=="archive") | .sha256')
vps# ACTUAL=$(sha256sum go${GO_VER}.linux-amd64.tar.gz | awk '{print $1}')
vps# [ "$EXPECTED" = "$ACTUAL" ] || { echo 'CHECKSUM MISMATCH'; exit 1; }

vps# rm -rf /usr/local/go
vps# tar -C /usr/local -xzf go${GO_VER}.linux-amd64.tar.gz
vps# ln -sf /usr/local/go/bin/go     /usr/local/bin/go
vps# ln -sf /usr/local/go/bin/gofmt  /usr/local/bin/gofmt
vps# go version    # → go version go1.25.10 linux/amd64
```

---

## 3. Build BeaconGate from master

```sh
vps# rm -rf /opt/beacongate-src
vps# git clone --depth 1 --branch master \
       https://github.com/trustwall1337/beacongate.git /opt/beacongate-src
vps# cd /opt/beacongate-src
vps# git log --oneline -1   # record the commit you built

vps# go build -trimpath -ldflags='-s -w' -o bin/beacongate-server ./cmd/beacongate-server
vps# go build -trimpath -ldflags='-s -w' -o bin/beacongate-admin  ./cmd/beacongate-admin
vps# go build -trimpath -ldflags='-s -w' -o bin/beacongate-client ./cmd/beacongate-client

vps# install -m755 bin/beacongate-server /usr/local/bin/
vps# install -m755 bin/beacongate-admin  /usr/local/bin/
vps# install -m755 bin/beacongate-client /usr/local/bin/
```

> Always build from a freshly-cloned `master`, not a dirty local tree.
> A binary built from uncommitted edits is not reproducible from the
> commit hash you recorded.

---

## 4. Create the system user and write `server_config.json`

```sh
vps# useradd --system --home /var/lib/beacongate --shell /usr/sbin/nologin beacongate
vps# install -d -m 750 -o beacongate -g beacongate /var/lib/beacongate
vps# install -d -m 750 -g beacongate /etc/beacongate
```

The `-g beacongate` on `/etc/beacongate` matters: with the dir owned
`root:root` and mode 750, the `beacongate` user cannot traverse into
it to read its own config.

Generate the master key and write the config:

```sh
vps# KEY=$(beacongate-admin gen-key)
vps# echo "Master AES key (save in a password manager): $KEY"

vps# cat > /tmp/server_config.json <<EOF
{
  "server_id": "beacongate-prod-1",
  "listen_addr": ":8080",
  "tunnel_path": "/tunnel",
  "health_path": "/healthz",
  "key": "$KEY",
  "policy": {
    "baseline_enabled": true,
    "store_path": "/var/lib/beacongate/policy.json"
  },
  "admin": {
    "enabled": true,
    "listen_addr": "127.0.0.1:9090",
    "token": ""
  },
  "client_template": {
    "listen_addr": "127.0.0.1:1080",
    "transport": {
      "type": "appsscript",
      "options": {
        "google_host": "216.239.38.120:443",
        "sni": "www.google.com,mail.google.com,accounts.google.com"
      }
    }
  }
}
EOF

vps# install -m640 -o beacongate -g beacongate /tmp/server_config.json /etc/beacongate/server_config.json
vps# rm /tmp/server_config.json
```

The `client_template` block is what `beacongate-admin add-client`
copies into each per-user config in §6. The `script_keys` array is
filled in later, once you've deployed the Apps Script forwarder.

---

## 5. Install the systemd unit and open the firewall

```sh
vps# install -m644 /opt/beacongate-src/ops/systemd/beacongate-server.service \
       /etc/systemd/system/
vps# systemctl daemon-reload
vps# systemctl enable --now beacongate-server
vps# systemctl is-active beacongate-server     # → active
vps# curl -fsS http://127.0.0.1:8080/healthz   # → ok
```

Two firewall layers must be open on TCP/8080:

```sh
# Host-level (ufw):
vps# apt-get install -y ufw
vps# ufw allow OpenSSH                        # do this BEFORE enable
vps# ufw allow 8080/tcp comment 'beacongate tunnel'
vps# echo y | ufw enable
```

The cloud-provider firewall (Hetzner Cloud Firewall, AWS Security
Group, GCP firewall rule, …) is a separate layer. Open TCP/8080 there
too. Forgetting the cloud layer is the most common cause of "curl to
the public IP times out" failures.

Once both are open, from the laptop:

```sh
mac% curl -fsS -m 10 http://YOUR.VPS.IP:8080/healthz   # → ok
```

In `appsscript` mode, only Google's outbound IP ranges actually need
to reach `:8080`. Once the system is verified working end-to-end,
narrow the source from `0.0.0.0/0` to Google's published ranges
(`_cloud.netblocks.google.com`).

---

## 6. Deploy the Apps Script forwarder

The forwarder runs inside Google's infrastructure and shuttles
encrypted batches between the BeaconGate client and your VPS.

1. Open <https://script.google.com/home> while signed into your
   personal Google account → **New project**.
2. Paste the contents of [apps_script/Code.gs](../apps_script/Code.gs)
   into the editor (`Cmd/Ctrl+A`, then paste).
3. Edit the `RELAY_URL` constant near the top to point at your VPS:
   ```javascript
   const RELAY_URL = 'http://YOUR.VPS.IP:8080/tunnel';
   ```
4. Rename the project (top-left) to something memorable, save with
   `Cmd/Ctrl+S`.
5. **Deploy → New deployment → ⚙ Select type → Web app.**
   - **Execute as:** Me
   - **Who has access:** Anyone *(required — without this, clients
     cannot post)*
6. **Deploy.** On first deploy Google asks for `UrlFetchApp`
   permission: *Authorize access* → your account → *Advanced → Go to
   {project} (unsafe) → Allow.*
7. Copy the **Deployment ID** (long base64 string starting with
   `AKfyc…`). The Script ID lives in the editor URL; the Deployment
   ID is in the Deploy panel and is what BeaconGate clients use.

Sanity-check by opening the Web app URL in a fresh tab:

```json
{"ok": true, "date": "2026-05-13", "count": 1, "version": 1, "protocol": 1}
```

HTML errors mean *Anyone* access is not set — redeploy.

If you misplace the Deployment ID: editor → **Deploy → Manage
deployments**.

---

## 7. Add the deployment to the server config

Edit `/etc/beacongate/server_config.json` and place the Deployment ID
in `client_template.transport.options.script_keys`:

```jsonc
"client_template": {
  "listen_addr": "127.0.0.1:1080",
  "transport": {
    "type": "appsscript",
    "options": {
      "script_keys": [
        {"id": "AKfyc...", "account": "your-personal-gmail"}
      ],
      "google_host": "216.239.38.120:443",
      "sni": "www.google.com,mail.google.com,accounts.google.com"
    }
  }
}
```

Restart the server so the new template is loaded:

```sh
vps# systemctl restart beacongate-server
vps# curl -fsS http://127.0.0.1:8080/healthz   # → ok
```

To extend the daily quota (~20,000 UrlFetchApp invocations per Google
account), deploy `Code.gs` in additional accounts and append more
entries to `script_keys`.

---

## 8. Generate a per-user config

```sh
vps# beacongate-admin add-client \
       --server-config /etc/beacongate/server_config.json \
       --name alice \
       --output /etc/beacongate/alice.json
vps# systemctl restart beacongate-server
```

This appends `alice` to the server's allowlist (each per-user master
key is independent — revoking one does not affect others) and writes
a ready-to-import JSON config. Copy it to the laptop for distribution:

```sh
mac% scp beacongate-vps:/etc/beacongate/alice.json /tmp/alice.json
mac% sha256sum /tmp/alice.json   # save the hash for out-of-band verification
```

Treat `alice.json` like a password — it contains the user's AES key.
Transfer over Signal, scp, or in person, never plain email.

---

## 9. Operator dry-run from the laptop

Before installing the APK on a phone, verify the full path works using
the same config from a laptop:

```sh
mac% beacongate-client -config /tmp/alice.json -control-addr 127.0.0.1:9091 &
mac% sleep 8     # transport preflight runs before SOCKS starts listening
mac% curl -fsS http://127.0.0.1:9091/api/status | jq '{state, transport_healthy, probe_ok}'
# expected: state in {connected, degraded}, transport_healthy=true, probe_ok=true

mac% curl --socks5-hostname 127.0.0.1:1080 -fsS https://api.ipify.org
# expected: returns YOUR.VPS.IP (8–10 s on a cold tunnel, ~2–4 s warm)
```

If `api.ipify.org` returns your VPS's IP, the full path — laptop →
uTLS → `script.google.com` → Apps Script → VPS → upstream → back —
works end-to-end. Stop the test client:

```sh
mac% pkill beacongate-client
```

---

## 10. Install the native Android app

Build the release APK on the laptop:

```sh
mac% cd /path/to/beacongate    # cloned from master
mac% make android-build-image  # one-time, ~10 min
mac% make android-apk
```

Plug in the phone over USB. On the phone, enable Developer Options
(Settings → About → tap *Build number* 7 times) and turn on **USB
debugging** under Developer Options. Accept the *Allow USB debugging?*
prompt the first time you connect.

```sh
mac% adb devices   # confirm the phone is listed
mac% adb install -r mobile/android/app/build/outputs/apk/release/app-arm64-v8a-release.apk
```

On the phone, open BeaconGate and import `alice.json`:

- **From Drive:** upload `alice.json` (*Anyone with the link can
  view*), paste the share URL in the app, tap **Fetch from Drive**.
- **From file picker:** copy `alice.json` to `Downloads/` (USB or
  Drive), tap **Pick from files**, select it.

Tap **Connect**, accept the system VPN dialog. Status flips to
**Connected**. Visit `https://api.ipify.org/` in Chrome — the IP shown
should match `YOUR.VPS.IP`.

The on-device walkthrough is in
[mobile/android/README.md](../mobile/android/README.md).

For environments where the APK cannot be installed (sideload blocked,
MDM, F-Droid-only), the legacy Termux + SOCKS5-client path is in
[android-termux.md](android-termux.md).

---

## Known limitations

- **Per-request latency floor of ~3–5 s** on the `appsscript`
  transport. The path runs `client → Google edge → Apps Script VM →
  VPS → upstream → back`, each leg adding overhead. v1.1.1 cut the
  legacy 8–10 s floor in half via server-side response folding; see
  [v1.1.1-latency-benchmark.md](v1.1.1-latency-benchmark.md).
- **Quota: ~20,000 UrlFetchApp invocations per Google account per
  day**, resetting at midnight US/Pacific. A single light user is
  comfortable within the ceiling; multiple users or sustained polling
  workloads need additional Google accounts in `script_keys`.
- **`coalesce_step_ms` should remain `0` (default).** Non-zero values
  have been observed to interact badly with the production
  active-drain window, causing SOCKS requests to time out. Re-evaluate
  after the bug is closed.
- **No graceful key rotation.** The wire envelope is keyed off the
  server's master AEAD key (and per-client HKDF derivations of it).
  Rotating the master is a hard cut: every existing client config
  stops working until you re-issue with `add-client`. Schedule
  rotations during a maintenance window.

The full residual-risk model (TLS fingerprinting, traffic-pattern
analysis, Google-side classifiers, URL-pattern blocking) is in
[SECURITY.md](../SECURITY.md).

---

## Routine maintenance

```sh
# rotate the master AES key
vps# NEW_KEY=$(beacongate-admin gen-key)
vps# jq --arg k "$NEW_KEY" '.key=$k' /etc/beacongate/server_config.json \
       > /tmp/sc.json && mv /tmp/sc.json /etc/beacongate/server_config.json
vps# chown beacongate:beacongate /etc/beacongate/server_config.json
vps# chmod 640 /etc/beacongate/server_config.json
vps# systemctl restart beacongate-server
# then re-run `add-client` for every active user, re-distribute the new JSON

# pull the latest master and rebuild from a clean tree
vps# cd /opt/beacongate-src && git pull --ff-only
vps# go build -o bin/beacongate-server ./cmd/beacongate-server
vps# install -m755 bin/beacongate-server /usr/local/bin/beacongate-server
vps# systemctl restart beacongate-server

# tail the server log
vps# journalctl -u beacongate-server -f

# inspect a specific user's sessions
vps# journalctl -u beacongate-server -o cat | grep '"client_id":"alice"'
```

For policy operations (allow / block / log-only rules), see
[policy.md](policy.md). For failure-mode triage, see
[troubleshooting.md](troubleshooting.md).

---

## Related

- [deployment.md](deployment.md) — terse two-playbook deployment reference (`https` and `appsscript`)
- [mobile/android/README.md](../mobile/android/README.md) — native Android app build + install
- [operator-handoff-checklist.md](operator-handoff-checklist.md) — pre-handoff verification
- [troubleshooting.md](troubleshooting.md) — failure-mode runbook
- [SECURITY.md](../SECURITY.md) — full residual-risk model
