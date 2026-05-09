# Getting started: from zero to a working BeaconGate tunnel

This guide walks you through setting up BeaconGate **end-to-end on a fresh Linux VPS**, deploying the Apps Script forwarder, building the Android handoff bundle, and getting your friend running on a phone in Termux + NekoBox/v2rayNG.

If you already have a server up and just need a config-shape reference, read [deployment.md](deployment.md) instead — that's the terse playbook. This guide is the hand-held, "I've never done this before" walkthrough that captures every gotcha I've hit in the field.

> Throughout this guide:
>
> - **OPERATOR** = you, the person running this guide. You own the VPS and the Google account hosting Apps Script.
> - **END USER** = your friend, the person in the censored country who runs the bundle on their phone.
> - Commands run **on your Mac/Linux laptop** are shown as `mac% command`.
> - Commands run **on the VPS** are shown as `vps# command` (root) or `vps$ command` (regular user).
> - Generic placeholders: `YOUR.VPS.IP`, `DEPLOYMENT_ID`, `BASE64_KEY`. Replace with your real values.

---

## Table of contents

- [What you need before you start](#what-you-need-before-you-start)
- [Step 1 — Get a VPS](#step-1--get-a-vps)
- [Step 2 — First SSH login](#step-2--first-ssh-login)
- [Step 3 — Install dependencies on the VPS](#step-3--install-dependencies-on-the-vps)
- [Step 4 — Clone and build BeaconGate](#step-4--clone-and-build-beacongate)
- [Step 5 — Generate the AES key and write server config](#step-5--generate-the-aes-key-and-write-server-config)
- [Step 6 — Install the systemd unit and start the server](#step-6--install-the-systemd-unit-and-start-the-server)
- [Step 7 — Open both firewall layers](#step-7--open-both-firewall-layers)
- [Step 8 — Verify reachability from your laptop](#step-8--verify-reachability-from-your-laptop)
- [Step 9 — Deploy the Apps Script forwarder](#step-9--deploy-the-apps-script-forwarder)
- [Step 10 — Build the client + write client_config.json](#step-10--build-the-client--write-client_configjson)
- [Step 11 — Operator dry-run on the VPS](#step-11--operator-dry-run-on-the-vps)
- [Step 12 — Build the Android handoff bundle](#step-12--build-the-android-handoff-bundle)
- [Step 13 — Self-test the bundle on a Linux box](#step-13--self-test-the-bundle-on-a-linux-box)
- [Step 14 — Transfer the bundle to the phone](#step-14--transfer-the-bundle-to-the-phone)
- [Step 15 — Phone-side end-user setup](#step-15--phone-side-end-user-setup)
- [Known limitations (read this before you ship)](#known-limitations-read-this-before-you-ship)
- [Routine maintenance](#routine-maintenance)
- [Troubleshooting common issues](#troubleshooting-common-issues)

---

## What you need before you start

### Hardware / accounts

- A Mac, Linux, or WSL laptop you'll run this guide from. Windows native is technically possible but the commands below assume a POSIX shell.
- A credit card to rent a VPS. Cheapest workable option: ~€5/month.
- A **Google account** that you'll use to host the Apps Script forwarder. **DO NOT use your work Google account.** A personal Gmail (or a fresh throwaway) is what you want — the script becomes tied to that identity, and Google can suspend it if their abuse heuristics flag it.
- Your **friend's phone** — must be Android. iOS doesn't have a working equivalent of Termux + NekoBox/v2rayNG that can consume a SOCKS5 server running locally without root.

### Software on your laptop

- `ssh`, `scp` (built into macOS and most Linux distros)
- `git`
- A text editor (anything that handles JSON without mangling whitespace)

That's it. **You don't need Go on your laptop** — we build everything on the VPS.

---

## Step 1 — Get a VPS

Any cloud provider works. The walkthrough below uses **Hetzner Cloud** because it's cheap (€4.75/month for 2 vCPU + 4 GB RAM), but DigitalOcean, Vultr, Linode etc. work the same way.

### Spec your server

Minimum that works comfortably:

| Resource | Minimum | Why |
|---|---|---|
| CPU | 1 vCPU | Server is mostly I/O-bound; one core is fine for one user |
| RAM | 1 GB | 512 MB technically works; 1 GB gives headroom for Go's GC |
| Disk | 20 GB | Bundle + binaries + logs |
| OS | Ubuntu 24.04 LTS | What we test against; other distros work but you'll adapt apt commands |
| Region | Outside the censored country | Obvious. Pick a region close to where you and your friend are for low TCP RTT to Google's edge. |

### Recommended: Hetzner Cloud `cx23`

1. Create a Hetzner account at <https://console.hetzner.com/>
2. Create a project (e.g. `beacongate`)
3. **Before creating the server**, add your SSH public key to **Project → Security → SSH Keys**:
   ```sh
   mac% cat ~/.ssh/id_ed25519.pub      # or whichever key you want to use
   ```
   Paste the entire line (including the `ssh-ed25519 …` prefix and trailing comment).
   - **Privacy gotcha**: the comment field at the end of your public key is plaintext on the server's `authorized_keys`. If your existing key was generated for a work email (`john@work.com`), don't use that key on personal infra — generate a fresh one:
     ```sh
     mac% ssh-keygen -t ed25519 -C "your-personal@gmail.com" -f ~/.ssh/id_ed25519_personal
     ```
4. **Create Server**. In the form:
   - **Image**: Ubuntu 24.04
   - **Type**: CX23 (or larger if you'll have many concurrent users)
   - **Networking**: defaults are fine; IPv4 + IPv6
   - **SSH Keys**: ✅ check the personal-key entry you just added
   - **Name**: `beacongate-server` (anything memorable)
5. Copy the IPv4 address Hetzner shows after creation — you'll use it everywhere below as `YOUR.VPS.IP`.

### If you forgot to attach the SSH key at creation

The Hetzner UI's "SSH Keys" registry only auto-installs keys onto **new** servers. Adding a key after the fact does NOT push it to existing servers. If you forget:
1. Find the root password Hetzner emailed you (subject "Cloud Server is ready"). If lost, use **Reset root password** in the Hetzner console.
2. SSH in with the password (Hetzner forces a password change on first login — see Step 2).
3. Manually paste the key into `/root/.ssh/authorized_keys` (instructions in Step 2).

---

## Step 2 — First SSH login

If you attached the SSH key during server creation, you can skip straight to:

```sh
mac% ssh -i ~/.ssh/id_ed25519_personal root@YOUR.VPS.IP "uname -a"
```

— if that returns the kernel version with no password prompt, jump to [Step 3](#step-3--install-dependencies-on-the-vps).

### If you didn't attach a key at creation

You'll need to install your key manually. The cleanest way:

```sh
mac% ssh root@YOUR.VPS.IP
```

You'll get a sequence of prompts. **Walk them in order**:

| Prompt | What to type |
|---|---|
| `root@YOUR.VPS.IP's password:` | the **OLD** Hetzner-emailed password |
| `Current password:` | the **OLD** Hetzner-emailed password (yes, twice) |
| `New password:` | a NEW strong password (save this in your password manager — you may need it once more) |
| `Retype new password:` | same NEW password |

Hetzner often disconnects you after the change. Reconnect with:

```sh
mac% ssh root@YOUR.VPS.IP
# enter the NEW password at the prompt
```

You should now be at a `root@beacongate-server:~#` shell. **Notice the `#` and the hostname — that's your "I'm on the VPS" indicator.** Confirm with:

```sh
vps# hostname && whoami
```

Now install your laptop's public key into `authorized_keys`:

```sh
vps# mkdir -p /root/.ssh && chmod 700 /root/.ssh
vps# cat >> /root/.ssh/authorized_keys <<'EOF'
ssh-ed25519 AAAA...your-personal-pubkey... your-personal@gmail.com
EOF
vps# chmod 600 /root/.ssh/authorized_keys
vps# exit
```

Verify key auth works without a password:

```sh
mac% ssh -i ~/.ssh/id_ed25519_personal root@YOUR.VPS.IP "hostname"
# expected: prints hostname, no password prompt
```

### Add an SSH alias for convenience

You'll be SSHing a lot. Add this to `~/.ssh/config` on your Mac:

```
Host beacongate-vps
    HostName YOUR.VPS.IP
    User root
    IdentityFile ~/.ssh/id_ed25519_personal
    IdentitiesOnly yes
```

Now `ssh beacongate-vps` is enough.

> **Common mistake**: when given a multi-line block like the heredoc above, people sometimes paste it on their **Mac** terminal instead of the **VPS** terminal. The prompt you see (`mac%` vs `vps#`) tells you where you are. If `mkdir -p /root/.ssh` errors out with "permission denied" on macOS, you ran it locally — re-SSH first.

---

## Step 3 — Install dependencies on the VPS

```sh
mac% ssh beacongate-vps
vps# apt-get update
vps# apt-get install -y git jq zip unzip ca-certificates curl
```

### Install Go 1.25.10

The repo's `go.mod` requires Go 1.25.10. Ubuntu 24.04's apt-shipped Go is older. Install from the official tarball:

```sh
vps# cd /tmp
vps# GO_VER=1.25.10
vps# curl -fsSLO https://go.dev/dl/go${GO_VER}.linux-amd64.tar.gz

# Verify SHA-256 against go.dev's published checksum (defense-in-depth on top of HTTPS):
vps# EXPECTED=$(curl -fsSL 'https://go.dev/dl/?mode=json&include=all' | \
       jq -r '.[] | select(.version=="go'${GO_VER}'") | .files[] | \
              select(.os=="linux" and .arch=="amd64" and .kind=="archive") | .sha256')
vps# ACTUAL=$(sha256sum go${GO_VER}.linux-amd64.tar.gz | awk '{print $1}')
vps# [ "$EXPECTED" = "$ACTUAL" ] || { echo 'CHECKSUM MISMATCH — DO NOT EXTRACT'; }

# Install:
vps# rm -rf /usr/local/go
vps# tar -C /usr/local -xzf go${GO_VER}.linux-amd64.tar.gz
vps# ln -sf /usr/local/go/bin/go /usr/local/bin/go
vps# ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
vps# rm -f go${GO_VER}.linux-amd64.tar.gz
vps# go version    # → "go version go1.25.10 linux/amd64"
```

---

## Step 4 — Clone and build BeaconGate

```sh
vps# mkdir -p /opt
vps# rm -rf /opt/beacongate-src
vps# git clone --depth 1 --branch master https://github.com/trustwall1337/beacongate.git /opt/beacongate-src
vps# cd /opt/beacongate-src
vps# git log --oneline -1     # confirm you're on a known commit

# Build server + admin:
vps# go build -trimpath -ldflags='-s -w' -o bin/beacongate-server ./cmd/beacongate-server
vps# go build -trimpath -ldflags='-s -w' -o bin/beacongate-admin  ./cmd/beacongate-admin

# Install onto $PATH:
vps# install -m755 bin/beacongate-server /usr/local/bin/beacongate-server
vps# install -m755 bin/beacongate-admin  /usr/local/bin/beacongate-admin

# Sanity check:
vps# beacongate-server -h | head -3
vps# beacongate-admin gen-key   # prints a base64 key — we'll use this in Step 5
```

---

## Step 5 — Generate the AES key and write server config

The **AES key** is the shared secret that authenticates clients to the server. The same value goes in **both** the server config (so the server can decrypt) and every client config (so clients can encrypt).

> Treat the key like a password. Don't commit it to git, don't paste it into Slack/Discord, don't send it over plain email. Hand it to your friend through an end-to-end encrypted channel (Signal, in person on a piece of paper, etc.).

### Create the system user and directories

The `beacongate` system user runs the server with reduced privileges. The systemd unit drops to it via `User=beacongate`.

```sh
vps# useradd --system --home /var/lib/beacongate --shell /usr/sbin/nologin beacongate
vps# install -d -m 750 -o beacongate -g beacongate /var/lib/beacongate
vps# install -d -m 750 -g beacongate /etc/beacongate
```

> **Gotcha**: if you set `/etc/beacongate` mode 750 owned by `root:root`, the `beacongate` user will fail to read its own config file inside that dir (no traversal permission). Above we `-g beacongate` so the dir's group is `beacongate` and the user's group permissions allow traversal.

### Generate the key and write the config

```sh
vps# KEY=$(beacongate-admin gen-key)
vps# echo "AES key (save this — clients need the same value): $KEY"

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
  }
}
EOF

vps# install -m640 -o beacongate -g beacongate /tmp/server_config.json /etc/beacongate/server_config.json
vps# rm -f /tmp/server_config.json
vps# cat /etc/beacongate/server_config.json    # verify the key landed
```

> **Save the key on your laptop too**: copy the base64 string into a password manager note titled "BeaconGate AES key". You'll need it for client configs and the Android bundle.

---

## Step 6 — Install the systemd unit and start the server

```sh
vps# install -m644 /opt/beacongate-src/ops/systemd/beacongate-server.service /etc/systemd/system/
vps# systemctl daemon-reload
vps# systemctl enable --now beacongate-server
vps# sleep 1
vps# systemctl is-active beacongate-server   # → "active"
vps# curl -fsS http://127.0.0.1:8080/healthz # → "ok"
```

If the server didn't start, look at the journal:

```sh
vps# journalctl -u beacongate-server --no-pager -n 30
```

Common first-time error — `permission denied` on the config file. The fix is in [Step 5](#step-5--generate-the-aes-key-and-write-server-config) (chgrp on the dir). If you missed it:

```sh
vps# chgrp beacongate /etc/beacongate
vps# systemctl reset-failed beacongate-server
vps# systemctl restart beacongate-server
```

---

## Step 7 — Open both firewall layers

There are usually **two firewall layers** between the public internet and your VPS:

1. **Cloud-provider firewall** (Hetzner Cloud Firewall, AWS Security Group, GCP firewall rule, etc.) — runs at the network level, before traffic reaches the VPS.
2. **`ufw` on the VPS itself** — Linux kernel-level firewall on the VPS.

You must open port **8080/tcp** on **both layers**. Forgetting the cloud layer is the single most common "why does `curl http://YOUR.VPS.IP:8080/healthz` time out" failure.

### `ufw` on the VPS

```sh
vps# apt-get install -y ufw
vps# ufw allow OpenSSH                                # CRITICAL — do this BEFORE enable
vps# ufw allow 8080/tcp comment 'beacongate tunnel'
vps# echo 'y' | ufw enable
vps# ufw status verbose
```

### Cloud-provider firewall (Hetzner specifically)

1. <https://console.hetzner.com/projects/YOUR_PROJECT/firewalls>
2. If your server has a firewall attached, click it. (If not, the cloud layer is wide-open and you can skip ahead.)
3. **Inbound Rules** tab → **Add Rule**:
   - Protocol: **TCP**
   - Port: `8080`
   - Source IPs: `0.0.0.0/0, ::/0`
   - Description: `beacongate tunnel`
4. **Save**.

The change takes effect immediately, no reboot needed.

> **Defence-in-depth note**: in `appsscript` mode, only Google's outbound IP ranges actually need to reach port 8080. After the system is verified working, narrow the source from `0.0.0.0/0` to Google's published ranges (`_cloud.netblocks.google.com`, etc.) — that drops public-internet exposure. Until then, keep it open for testing.

---

## Step 8 — Verify reachability from your laptop

```sh
mac% curl -fsS -m 10 http://YOUR.VPS.IP:8080/healthz
# expected: "ok"
```

If this hangs at 10s with `Failed to connect`, you have a firewall problem. Most likely cause: cloud-provider firewall isn't open. Re-check Step 7's cloud-firewall section.

---

## Step 9 — Deploy the Apps Script forwarder

This is the piece that makes the tunnel look like ordinary Google traffic. It runs **inside Google's infrastructure** and forwards encrypted batches between the BeaconGate client and your VPS.

### 9.1 Switch to your personal Google account

Open <https://myaccount.google.com/> in a clean browser session. Confirm the top-right shows the personal Gmail you want to use, NOT a work account. Switch accounts if needed.

### 9.2 Render `Code.gs` with your VPS IP

The forwarder script needs `RELAY_URL` set to your VPS. You can either edit the file in the Apps Script editor manually, or pre-render it on the VPS so you can copy-paste the whole file in one shot:

```sh
vps# sed "s|http://YOUR.VPS.IP:8080/tunnel|http://YOUR.VPS.IP:8080/tunnel|" \
       /opt/beacongate-src/apps_script/Code.gs > /tmp/Code.gs
# (Replace YOUR.VPS.IP with your actual VPS IP in the sed pattern.)

vps# cat /tmp/Code.gs    # copy the entire output to your clipboard
```

Or on your **laptop**:

```sh
mac% ssh beacongate-vps "cat /opt/beacongate-src/apps_script/Code.gs" | \
       sed "s|http://YOUR.VPS.IP:8080/tunnel|http://YOUR.VPS.IP:8080/tunnel|" | pbcopy
# now Code.gs (with RELAY_URL substituted) is on your clipboard
```

### 9.3 Create the project

1. <https://script.google.com/home> → **New project** (top-left).
2. Click anywhere in the editor → **Cmd/Ctrl+A** (select all) → **Cmd/Ctrl+V** (paste).
3. Around line ~33 you should see:
   ```javascript
   const RELAY_URL = 'http://YOUR.VPS.IP:8080/tunnel';
   ```
   With your actual VPS IP. Confirm.
4. Top of page → click "Untitled project" → rename to something memorable (e.g. `beacongate-relay-1`). Save with **Cmd/Ctrl+S**.

### 9.4 Deploy as a Web app

1. Top-right blue button: **Deploy** → **New deployment**.
2. Click the **gear icon ⚙ next to "Select type"** → choose **Web app**.
3. Settings:
   - **Description**: `v1` (or anything)
   - **Execute as**: **Me** (your account)
   - **Who has access**: **Anyone** ← *critical*; without this, BeaconGate clients can't post
4. **Deploy**.

### 9.5 Authorize on first deploy

Google will ask for `UrlFetchApp` permission (the script needs to make outbound calls to your VPS):
1. **Authorize access** → pick your Google account
2. You'll see *"Google hasn't verified this app"* — that's normal, it's *your* app.
3. **Advanced** → **Go to your-project-name (unsafe)** → **Allow**.

### 9.6 Copy the Deployment ID

After the deploy, a panel shows:
- **Deployment ID** ← copy this. Long base64-ish string starting with `AKfyc…`. **NOT the Script ID** (the Script ID is in the URL of the editor; the Deployment ID is in this panel and in the Web app URL).
- **Web app URL** — `https://script.google.com/macros/s/<DEPLOYMENT_ID>/exec`

### 9.7 Sanity check

Open the Web app URL in a fresh browser tab. You should see JSON like:

```json
{"ok":true,"date":"2026-05-09","count":1,"version":1,"protocol":1}
```

If you see this, the script is live. If you get an HTML error page instead, redeploy with `Anyone` access (Step 9.4).

> **If you forget the Deployment ID**: editor → **Deploy** → **Manage deployments** → it's listed next to each active deployment.

> **Save the Deployment ID on your laptop**: alongside the AES key. You'll bake both into client configs.

---

## Step 10 — Build the client + write `client_config.json`

Build the client binaries on the VPS too — keeps everything consistent with master:

```sh
vps# cd /opt/beacongate-src
vps# go build -trimpath -ldflags='-s -w' -o bin/beacongate-client ./cmd/beacongate-client
vps# GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' \
       -o bin/beacongate-client-android-arm64 ./cmd/beacongate-client
vps# ls -lh bin/beacongate-client*
```

Now write the client config. Use your AES key from Step 5 and your Deployment ID from Step 9:

```sh
vps# KEY=$(jq -r '.key' /etc/beacongate/server_config.json)
vps# DEPLOYMENT_ID="AKfyc..."   # paste yours

vps# cat > /opt/beacongate-src/client_config.json <<EOF
{
  "client_id": "client-friend-name-1",
  "listen_addr": "127.0.0.1:1080",
  "server": {
    "key": "$KEY"
  },
  "transport": {
    "type": "appsscript",
    "options": {
      "script_keys": [
        {"id": "$DEPLOYMENT_ID", "account": "your-personal-gmail"}
      ],
      "google_host": "216.239.38.120:443",
      "sni": "www.google.com,mail.google.com,accounts.google.com"
    }
  }
}
EOF

vps# chmod 600 /opt/beacongate-src/client_config.json

# Validate the config:
vps# /opt/beacongate-src/bin/beacongate-client -config /opt/beacongate-src/client_config.json -validate-only
# expected: {"client_id":"...","listen_addr":"127.0.0.1:1080","ok":true,"transport":"appsscript"}
```

> **DO NOT enable `coalesce_step_ms`** — there is a known production bug where any non-zero value causes 100% of SOCKS curls to time out. See [Known limitations](#known-limitations-read-this-before-you-ship).

---

## Step 11 — Operator dry-run on the VPS

The handoff checklist (`docs/operator-handoff-checklist.md`) is firm: **never ship a bundle you haven't booted from cold yourself**. Run the client on the VPS itself first to confirm everything talks.

```sh
# Spawn the client as a transient systemd unit:
vps# systemd-run --unit=bg-test-client \
       --working-directory=/opt/beacongate-src --service-type=simple \
       /opt/beacongate-src/bin/beacongate-client \
       -config /opt/beacongate-src/client_config.json \
       -control-addr 127.0.0.1:9091

# Wait for preflight (the client probes the relay before listening on SOCKS5):
vps# sleep 12

# Check status:
vps# curl -fsS http://127.0.0.1:9091/api/status | jq '{state, transport_healthy, probe_ok}'
# expected: state in {connected, degraded}, transport_healthy=true, probe_ok=true
```

Now exercise the SOCKS5 path:

```sh
# This proves the FULL TUNNEL works:
#   VPS-client → uTLS → Google (script.google.com) → Apps Script → VPS-server →
#   api.ipify.org → response back through the same path
vps# time curl --socks5-hostname 127.0.0.1:1080 -fsS -m 30 https://api.ipify.org
# expected: prints YOUR.VPS.IP after 8-10 seconds (the structural latency floor — see
# Known limitations)
```

If you got `YOUR.VPS.IP` in the output, the full path works. The tunnel is verified.

```sh
# Stop the test client:
vps# systemctl stop bg-test-client.service
```

---

## Step 12 — Build the Android handoff bundle

```sh
vps# cd /opt/beacongate-src
vps# mkdir -p /tmp/bundles
vps# ops/prepare-bundle.sh \
       --binary bin/beacongate-client-android-arm64 \
       --config client_config.json \
       --vps-ip YOUR.VPS.IP \
       --out /tmp/bundles/beacongate-bundle-$(date +%Y%m%d).zip
```

Output:

```
==> validating config with bin/beacongate-client
{"client_id":"...","listen_addr":"127.0.0.1:1080","ok":true,"transport":"appsscript"}
==> bundle: /tmp/bundles/beacongate-bundle-20260509.zip
    size:   3245343 bytes
    sha256: 184667...

Hand the .zip to your friend. The SHA-256 above is what you'll
compare out-of-band to confirm they received the file you sent.
```

**Save the SHA-256 on your laptop** — you'll read it to your friend over a separate channel (voice call, Signal, etc.) so they can verify the bundle they receive matches what you sent.

The bundle contains:

| File | Purpose |
|---|---|
| `beacongate-client-android-arm64` | the cross-compiled Linux/arm64 client binary (Android phones are arm64) |
| `client_config.json` | with the AES key and Deployment ID baked in |
| `verify.sh` | the phone-side end-to-end check |
| `README.txt` | terse phone-side setup steps |

---

## Step 13 — Self-test the bundle on a Linux box

> Quoting `docs/operator-handoff-checklist.md`: *"Don't ship a bundle you haven't yourself booted from cold."*

Use the VPS itself as your "Linux box" — saves you needing a separate machine:

```sh
vps# apt-get install -y qemu-user-static    # so we can exec the arm64 binary on amd64
vps# mkdir -p /tmp/bundle-test && cd /tmp/bundle-test
vps# unzip -o /tmp/bundles/beacongate-bundle-*.zip

# The bundled arm64 binary should execute via qemu:
vps# qemu-aarch64-static ./beacongate-client-android-arm64 -h | head -3
# expected: prints the help text — proves the cross-compile is valid

# Run verify.sh against the still-running test client (or start a fresh one):
vps# bash verify.sh
# expected: 2 passed, 1 failed
# the failing one is the disguise check — it ALWAYS fails on the VPS dry-run
# because the server runs HERE (the local addr matches $VPS_IP). It will
# pass on the real phone, which never has a direct conn to the VPS.
```

If `verify.sh` shows **`2 passed, 1 failed`** with **only** the disguise check failing, your bundle is good to ship. If checks 1 or 2 fail, fix the underlying issue before sending.

---

## Step 14 — Transfer the bundle to the phone

Copy the bundle to your laptop first:

```sh
mac% scp beacongate-vps:/tmp/bundles/beacongate-bundle-*.zip ~/Desktop/
mac% shasum -a 256 ~/Desktop/beacongate-bundle-*.zip
# verify this matches what prepare-bundle.sh printed in Step 12
```

From your laptop, get it to your friend's phone. Pick whichever channel is appropriate:

- **In person**: USB cable, AirDrop (if iOS receiver — though end-user is Android), Bluetooth. Most secure if you're in the same room.
- **Remote**: end-to-end encrypted messenger (Signal "send file"), encrypted email attachment, Tresorit-style file transfer service.
- **Avoid**: SMS, MMS, WhatsApp media (lossy compression may corrupt zip), email without encryption, public file-sharing.

**Out of band, give your friend the SHA-256.** Voice call is best. They'll compare it on the phone before unzipping.

---

## Step 15 — Phone-side end-user setup

This is **your friend's part**. There are two ways to do this:

- **(A) One-paste install** (recommended) — operator generates a single command (or QR code), friend pastes/scans it once in Termux, done. ~3 taps total.
- **(B) Manual bundle unpack** — operator sends the `.zip` from Step 12, friend runs ~6 commands. The fallback if the friend can't reach github.com from Termux directly.

### Path A — One-paste install (recommended)

The operator runs `beacongate-admin export-link` with a flag that produces a single shell command (or a PNG QR code containing that command) bundling the friend's config and the binary download in one. The friend scans/pastes once.

#### A.1 Operator generates the install QR

> **Prerequisite:** the repo must have at least one published GitHub Release with the `BeaconGate-<tag>-android-arm64.tar.gz` archive uploaded (the release pipeline at [`.github/workflows/release.yml`](../.github/workflows/release.yml) does this on every `v*` tag push). The Termux installer downloads the binary from there.

On your laptop or VPS:

```sh
beacongate-admin export-link \
  --config client_config.json \
  --install-qr-png /tmp/install-qr.png

# stdout: the bare bg:// link (treat as sensitive)
# stderr: a sensitive-data warning + "install-cmd qr png written to /tmp/install-qr.png"
```

Or to get a Unicode-block QR straight in your terminal:

```sh
beacongate-admin export-link --config client_config.json --install-qr
```

Send `/tmp/install-qr.png` to the friend over an end-to-end encrypted channel (Signal, in person on a USB stick, etc.). **The QR contains the AES key — treat it like a password.**

> The QR encodes a `curl … | bash -s -- --import "bg://…"` line, around ~600 characters. Phone cameras decode this reliably under indoor lighting in a Version-25 QR.

#### A.2 Friend installs Termux

1. Install Termux **from F-Droid** (NOT Google Play — the Play version is outdated):
   - <https://f-droid.org/packages/com.termux/>
2. Open Termux.

#### A.3 Friend scans the QR

1. Friend points their phone camera (or any QR scanner) at the PNG you sent. The scanner detects text starting with `curl -fsSL …`.
2. Friend taps **Copy** (or "Share to Termux" if their scanner supports Android intents).
3. Friend pastes into Termux (long-press → Paste) and hits Enter.

That single line:

- Installs Termux deps (`curl`, `unzip`, `jq`, `termux-api`)
- Asks Android for storage permission (one-time)
- Resolves the latest BeaconGate release tag from GitHub
- Downloads `beacongate-client-android-arm64` from that release
- Decodes the bg:// share-link → writes `client_config.json`
- Runs `termux-wake-lock` so Android's battery optimizer doesn't kill it
- Starts the client in the background
- Waits for preflight, prints next-step instructions

Total time: ~30–60 seconds depending on phone network. The script prints a clear "✅ BeaconGate is running" banner with the SOCKS5 address when ready, or a friendly error message with what went wrong.

#### A.4 Wire up NekoBox or v2rayNG

Same as Path B step 15.4 below — install from F-Droid, point a SOCKS5 inbound at `127.0.0.1:1080`, tap Connect.

#### A.5 Confirm

```sh
# On the phone, in Termux:
curl -x socks5h://127.0.0.1:1080 https://api.ipify.org
# expected: prints YOUR.VPS.IP (NOT the phone's mobile-data IP)
```

---

### Path B — Manual bundle unpack (fallback)

Use this if the friend's network can't reach github.com directly (some censors block GitHub) or the `--install-qr-png` flow has any issue. Send them [docs/android-termux.md](android-termux.md) — the canonical end-user doc — and the bundle from Step 12.

#### B.1 Termux

1. Install Termux **from F-Droid** (NOT Google Play): <https://f-droid.org/packages/com.termux/>
2. Open Termux. First-run housekeeping:
   ```sh
   pkg update && pkg upgrade
   pkg install unzip openssl
   termux-setup-storage    # grants Termux access to /sdcard, then ~/storage symlinks
   ```

#### B.2 Verify and unpack the bundle

The phone has the zip somewhere — usually in `~/storage/downloads/` (where Android put it after the file transfer).

```sh
# On the phone in Termux:
cd ~/storage/downloads
sha256sum beacongate-bundle-*.zip
# Friend reads this back to operator over voice; operator confirms it matches.
# IF MISMATCH: STOP. Get a fresh copy of the bundle.

mkdir -p ~/beacongate
unzip beacongate-bundle-*.zip -d ~/beacongate
cd ~/beacongate
chmod +x beacongate-client-android-arm64
```

#### B.3 Start the client

```sh
# CRITICAL: prevents Android's battery optimizer from killing Termux
termux-wake-lock

# Start the client in the background:
nohup ./beacongate-client-android-arm64 \
  -config client_config.json \
  -control-addr 127.0.0.1:9091 > client.log 2>&1 &

# Wait ~10s for preflight to complete:
sleep 10

# Run the verify.sh self-test:
bash verify.sh
# expected: "3 passed, 0 failed"
```

#### B.4 Wire up NekoBox or v2rayNG

The client is now a SOCKS5 server on `127.0.0.1:1080`. Point a SOCKS5 client at it:

**NekoBox** (recommended; easier UI):
1. Install from F-Droid: <https://f-droid.org/packages/io.nekohasekai.sfa/>
2. **Profiles → New** → **SOCKS5**:
   - Server: `127.0.0.1`
   - Port: `1080`
3. Tap to connect.
4. Browser/app traffic now flows through the BeaconGate tunnel.

**v2rayNG**: similar idea, see [docs/android-termux.md](android-termux.md) for the exact field-by-field setup.

#### B.5 Confirm

```sh
# On the phone, in Termux:
curl -x socks5h://127.0.0.1:1080 https://api.ipify.org
# expected: prints YOUR.VPS.IP (NOT the phone's mobile-data IP)
```

---

## Known limitations (read this before you ship)

These are honest current-state limitations of master as of 2026-05-09. None are blockers for getting a tunnel up, but they shape end-user expectations.

### Per-request latency floor: ~3–5s (down from ~8–10s pre-v1.1.1)

v1.1.1 ships a protocol-level round-trip fusion that collapses three sequential Apps Script invocations per logical SOCKS request into one. Breakdown today:

```
1× POST (OPEN+DATA fused via Dial-side coalesce) → ~1.8s   Apps Script per-call overhead
   server holds the same POST until upstream replies (~200–800ms typical)
─────────────────────────────────
~2–3s minimum, observed 3–5s in practice
```

The 22% drop in Apps Script invocations per request is also a real quota win — each request burns one POST instead of three.

**What this means in practice:**

| Workload | Verdict |
|---|---|
| One-shot API call / `curl example.com` | Fine. ~3s feels prompt. |
| Messaging app (WhatsApp, Signal, Telegram) | Fine. Initial connection is fast; after that, the persistent connection multiplexes and feels normal. |
| Email sync (IMAP/POP3) | Fine. |
| SSH | Comfortable for interactive use. Bulk file transfer remains slow because each round-trip pays the per-call floor. |
| Browser page load | Workable. TLS handshake still costs several round-trips through the tunnel (~10–15s for a fresh connection). Resources on the same page reuse the connection and feel normal. |
| Video / audio streaming | **Don't.** |

### `coalesce_step_ms` is still broken in production

The client config has a knob `coalesce_step_ms` documented as a quota-saving optimization. **Do not enable it.** With any non-zero value, 100% of SOCKS5 curls time out at 25s in production (against real Apps Script). Unit tests in CI pass, but the bug only manifests with the real Apps Script transport. The fusion shipped in v1.1.1 (Dial-side OPEN+DATA buffer) bypasses this machinery entirely, so the latency win arrives without enabling `coalesce_step_ms`. Leave it out of `client_config.json` until the underlying bug is fixed.

### Apps Script daily quota

Each Google account has ~20,000 `UrlFetchApp` invocations per day. Resets at **midnight Pacific** (10:30 AM Iran time). Under typical end-user load (one user, normal browsing/messaging), this lasts the whole day. If your friend hits the cap, the script returns 403 and the client backs off for 30 minutes.

To raise the ceiling, deploy `Code.gs` in **additional** Google accounts and add their Deployment IDs to `script_keys` in `client_config.json`. The client round-robins across them.

---

## Routine maintenance

### Watch the server logs while your friend tests

```sh
mac% ssh beacongate-vps "journalctl -u beacongate-server -f"
# stream live; expect session.open / session.upstream_eof for each curl on the phone
# Ctrl-C to stop streaming.
```

### Rotate the AES key every few months

```sh
vps# beacongate-admin gen-key                                      # new key
vps# # update /etc/beacongate/server_config.json with the new key
vps# systemctl restart beacongate-server
# regenerate client_config.json + bundle (Step 10–13), hand to friend
```

The old key is hard-cut — anyone running the old bundle stops working as soon as the server has the new key.

### Upgrade BeaconGate

```sh
vps# cd /opt/beacongate-src
vps# git pull --ff-only origin master
vps# go build -trimpath -ldflags='-s -w' -o bin/beacongate-server ./cmd/beacongate-server
vps# go build -trimpath -ldflags='-s -w' -o bin/beacongate-admin  ./cmd/beacongate-admin
vps# install -m755 bin/beacongate-server /usr/local/bin/
vps# install -m755 bin/beacongate-admin /usr/local/bin/
vps# systemctl restart beacongate-server
```

For a major version bump, also rebuild the Android client + ship a new bundle:

```sh
vps# go build -trimpath -ldflags='-s -w' -o bin/beacongate-client ./cmd/beacongate-client
vps# GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' \
       -o bin/beacongate-client-android-arm64 ./cmd/beacongate-client
vps# ops/prepare-bundle.sh \
       --binary bin/beacongate-client-android-arm64 \
       --config client_config.json \
       --vps-ip YOUR.VPS.IP \
       --out /tmp/bundles/beacongate-bundle-$(date +%Y%m%d).zip
```

---

## Troubleshooting common issues

| Symptom | Likely cause | First step |
|---|---|---|
| `curl http://YOUR.VPS.IP:8080/healthz` times out | Cloud-provider firewall closed | [Step 7](#step-7--open-both-firewall-layers) — open 8080 in Hetzner Cloud Firewall |
| Server restart loops with `permission denied` on config | `/etc/beacongate` group ownership wrong | `chgrp beacongate /etc/beacongate` (Step 5) |
| Apps Script `/exec` returns HTML error page instead of JSON | Web app not deployed with `Anyone` access | Re-deploy ([Step 9.4](#94-deploy-as-a-web-app)) |
| Apps Script `/exec` returns `Moved Temporarily` | Normal — `script.google.com` always 302-redirects to `script.googleusercontent.com`. The client follows it. |
| Client status shows `state=degraded`, all curls timing out | Connection wedge after extended use (rare since v1.1.1: pool retires conns every 5 min and on any RoundTrip error) | Wait one minute for auto-recovery; if it persists, restart the client |
| `auth failed` (HTTP 401) on `/tunnel` | Server and client AES keys diverged | Regenerate key, redo Steps 5+10+12 |
| `RESET POLICY_DENIED` on legitimate destination | Server's policy engine blocked it | `beacongate-admin put-rule --addr http://127.0.0.1:9090 --file allow-rule.json`; see [policy.md](policy.md) |
| Friend's phone shows `verify.sh: 2/3 passed, only #3 disguise failing` | Likely OK — that check has known flakiness on first cold start. Have them re-run after any successful curl. |
| All friend's curls timeout after first few minutes | Possible connection wedge (rare since v1.1.1; the pool now retires conns every 5 min and immediately on RoundTrip error) | If self-recovery doesn't kick in within a minute, restart the client (`pkill beacongate-client && nohup ./beacongate-client-android-arm64 ... &`) |

For deeper failure-mode runbooks, see [troubleshooting.md](troubleshooting.md).

---

## Where to read next

- [docs/architecture.md](architecture.md) — system overview, glossary, end-to-end diagrams. Read this if you want to understand WHY the protocol is shaped this way.
- [docs/protocol.md](protocol.md) — wire-protocol spec.
- [docs/deployment.md](deployment.md) — terse two-playbook reference (`https` vs `appsscript`). Read this when you've done the walkthrough once and want a quicker setup the second time.
- [docs/operator-handoff-checklist.md](operator-handoff-checklist.md) — the formal pre-handoff verification.
- [docs/android-termux.md](android-termux.md) — the doc to send your friend.
- [docs/policy.md](policy.md) — adding/removing/auditing server-side outbound policy rules.
- [docs/admin-api.md](admin-api.md) — the admin HTTP surface.
- [docs/troubleshooting.md](troubleshooting.md) — full failure-mode runbook.
- [SECURITY.md](../SECURITY.md) — residual-risk model + how to report vulnerabilities.
