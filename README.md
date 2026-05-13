# BeaconGate

**BeaconGate** is a transport-agnostic SOCKS5 tunnel that routes traffic
through Google Apps Script so a network observer only sees TLS to a
Google IP. Bytes are AES-256-GCM sealed end-to-end with per-client
HKDF-derived keys; Google never holds the key and never sees plaintext.
The TLS handshake uses [uTLS](https://github.com/refraction-networking/utls)
to mimic a Chrome 131 ClientHello, reducing naive JA3/JA4
distinguishability.

It is built around a transport-agnostic engine with replay protection,
AAD-bound client identity, and server-side outbound policy enforcement.

---

## ⚠️ What this is NOT

Before you install BeaconGate, understand what it does NOT defend
against. End users deserve informed consent.

- **Traffic-pattern analysis.** A sophisticated observer with full
  packet capture can fingerprint long-poll cadence, payload-size
  distribution, and per-batch timing — even though the bytes are
  encrypted. They may infer *that* you're tunneling, even if not *what*.
- **Google-side classifiers.** Google can flag and disable Apps Script
  projects that match abuse patterns. We have no control over this.
- **URL-pattern blocking.** A censor can block
  `script.google.com/macros/s/.../exec` paths specifically, with broad
  collateral damage to legitimate Apps Script users.
- **TLS-fingerprint analysis (with caveats).** v1.1.0 closes the
  default-build gap with uTLS, but pinning regression, stdlib fallback,
  or new fingerprinting techniques could re-open it. Pin cadence tracked
  in [docs/uTLS-fingerprint-cadence.md](docs/uTLS-fingerprint-cadence.md).

**BeaconGate raises the cost of blocking. It does not make blocking
impossible.** If you need a tool that survives a state-level adversary,
use Tor with bridges instead.

See [SECURITY.md](SECURITY.md) for the full residual-risk model.

---

## How it works

```
[ your apps ]  ←SOCKS5→  [ BeaconGate client ]  ←Apps Script tunnel→  [ BeaconGate server ]  ←TCP→  [ destinations ]
                          (laptop or phone)        (looks like Google)    (your VPS)
```

Local apps point their SOCKS5 setting at the BeaconGate client (default
`127.0.0.1:1080`). The client wraps each batch in an AES-256-GCM sealed
envelope and POSTs it to a Google Apps Script web app deployed under
your own Google account. The Apps Script forwards the bytes to your VPS,
which decrypts, runs server-side outbound policy (no torrents, no SSRF,
no metadata IPs), dials the destination, and pipes bytes back the same
way.

A network observer between you and Google sees what looks like Chrome
making ordinary HTTPS requests to `script.google.com`. They see neither
the destination, nor the plaintext, nor the BeaconGate server's IP. Full
picture in [docs/architecture.md](docs/architecture.md).

---

## Transport modes

| Mode | What it does | Use it when |
| --- | --- | --- |
| **`appsscript`** | Tunnels every batch through a user-deployed Google Apps Script web app. Wire path terminates at a real Google IP with `SNI=www.google.com` (uTLS-fingerprinted as Chrome 131) and HTTP `Host: script.google.com`. | You need traffic that looks like ordinary Google traffic to a network observer. **This is the censorship-evasion path.** |
| `https` | Direct HTTPS POST to an operator-configured URL. Generic HTTPS, **NOT a censorship-evasion path on its own**. | You operate your own relay behind a CDN / your own domain fronting, or you don't need on-path-censor evasion. |

---

## Setup

A working tunnel has four moving parts. Each section is independent —
do them in order.

### 1. Server (your VPS, outside the censored network)

Run the one-liner installer on a Linux VPS:

```sh
curl -fsSL https://raw.githubusercontent.com/trustwall1337/beacongate/master/scripts/install.sh | sudo bash
```

The installer downloads the signed release tarball, verifies its cosign
signature against this repo's GitHub Actions workflow, generates an
AES-256 key, writes `/etc/beacongate/server_config.json` (mode `0600`),
and starts a systemd unit.

For a manual install, Docker Compose, or reverse-proxy TLS setup, see
[docs/deployment.md](docs/deployment.md).

### 2. Apps Script (your Google account)

1. Open <https://script.google.com/> → **New project**.
2. Paste the contents of [apps_script/Code.gs](apps_script/Code.gs).
3. Edit `RELAY_URL` to point at `http://YOUR.VPS.IP:8080/tunnel`.
4. **Deploy → New deployment → Web app → Execute as Me, Anyone has access.**
5. Copy the **Deployment ID** (not the Script ID — they look similar).

Every time you edit `Code.gs`, create a **new** deployment and update
the client's `script_keys`. Saving alone does not propagate.

### 3. Client config

Use `beacongate-admin add-client` on the VPS to generate a per-client
config JSON file:

```sh
sudo beacongate-admin add-client \
  --server-config /etc/beacongate/server_config.json \
  --name alice \
  --output alice.json
sudo systemctl restart beacongate-server
```

This appends `alice` to the server's allowlist and writes a ready-to-use
JSON config containing the per-client key, the server URL, and the
transport options. Copy that JSON file to whichever device will run the
BeaconGate client.

> The JSON contains an AES key. Treat it like a password — transfer over
> Signal, scp, or in person; never over plaintext channels.

### 4. Client (laptop or phone)

#### Laptop (macOS / Linux / Windows)

Download the matching release tarball from
[GitHub Releases](https://github.com/trustwall1337/beacongate/releases),
verify the cosign signature (command in the release notes), then:

```sh
beacongate-client -config alice.json -control-addr 127.0.0.1:9091
```

Point any SOCKS5-capable client at `127.0.0.1:1080`.

#### Android (native app)

The Android app is a system VPN service that captures all phone traffic
into the tunnel. No Termux, no manual SOCKS5 wiring on the phone.

1. Build the release APK on your laptop:
   ```sh
   make android-build-image   # one-time, ~10 min
   make android-apk           # produces a release APK (~15 MB)
   ```
2. Install on the phone via `adb`:
   ```sh
   adb install -r mobile/android/app/build/outputs/apk/release/app-arm64-v8a-release.apk
   ```
3. Transfer `alice.json` to the phone (Drive link or USB copy to
   `Downloads/`).
4. Open BeaconGate on the phone → import the JSON file → **Connect**,
   then accept the system VPN dialog.

Full build + install walkthrough in
[mobile/android/README.md](mobile/android/README.md). For the legacy
Termux path, see [docs/android-termux.md](docs/android-termux.md).

---

## Quick start

Once setup is done, verify the tunnel from a laptop:

```sh
beacongate-client -config alice.json -control-addr 127.0.0.1:9091
curl -x socks5h://127.0.0.1:1080 https://api.ipify.org
```

The returned IP should be your VPS's public IP. If it is, the tunnel is
working end-to-end.

---

## Configuration

Examples in this repo:

- [server_config.example.json](server_config.example.json)
- [client_config.example.json](client_config.example.json) — `https` mode
- [client_config.appsscript.example.json](client_config.appsscript.example.json) — `appsscript` mode

### `script_keys`

Both shapes are accepted:

```json
"script_keys": [
  {"id": "DEPLOYMENT_ID_1", "account": "alpha-account"},
  {"id": "DEPLOYMENT_ID_2", "account": "beta-account"}
]
```

```json
"script_keys": "DEPLOYMENT_ID_1,DEPLOYMENT_ID_2"
```

Migrate older configs with `beacongate-admin migrate-config --file client_config.json`.

### `coalesce_step_ms` (adaptive uplink coalescing)

For interactive workloads (SSH, IRC, REST polling), the default fires
one HTTP POST per outbound frame, which can drain a single Google
account's daily quota fast. Setting `coalesce_step_ms` to a positive
value batches outbound frames; trades latency for ~80% fewer POSTs.

```json
{ "coalesce_step_ms": 30 }
```

- `0` (default) — off.
- `20–40` — recommended starting range.
- Max `200` — hard cap; values above hurt responsiveness.

### SOCKS5 username/password

Empty username = no auth (default, safe for loopback). **Set
`socks.username` and `socks.password` whenever you bind to a non-loopback
address**, or anyone on the LAN can drain your quota.

```json
{
  "listen_addr": "0.0.0.0:1080",
  "socks": { "username": "alice", "password": "long-random-shared-secret" }
}
```

---

## Multiple deployments, multiple accounts

Apps Script enforces ~20,000 UrlFetchApp invocations/day per Google
account (resets at midnight Pacific). Multi-deployment failover is the
answer:

1. Deploy `Code.gs` in additional Google accounts.
2. Add their Deployment IDs to `script_keys` with distinct `account`
   labels. The client round-robins and fails over on per-deployment
   403/quota errors.
3. Quota usage is reported per account in the client's diagnostic
   output.

---

## Troubleshooting

Full failure-mode runbook: [docs/troubleshooting.md](docs/troubleshooting.md).

| Symptom | First step |
| --- | --- |
| `auth failed` (HTTP 401) | Server and client AEAD keys diverged. Regenerate and roll. |
| `transport.healthy=false` | Network unreachable (`https`) or Apps Script deployment broken (`appsscript`). |
| `RESET POLICY_DENIED` | Server policy is blocking the destination. Add an allow rule. |
| All deployments quota-blacklisted | Wait until midnight Pacific or add more Google accounts to `script_keys`. |
| Pasted Script ID instead of Deployment ID | They look similar; the Apps Script console's deployments page shows the Deployment ID. |
| Edited `Code.gs` and forgot to redeploy | Saving the file does not update what clients see. Deploy → New deployment. |

---

## Security defaults

- **TLS 1.3 minimum** on both transports.
- **uTLS Chrome 131 ClientHello** on `appsscript` — mimics a real Chrome
  handshake to reduce naive JA3/JA4 distinguishability. Pin tracked in
  [docs/uTLS-fingerprint-cadence.md](docs/uTLS-fingerprint-cadence.md).
- **AES-256-GCM** authenticated encryption per batch, with **per-client
  HKDF-derived keys** — a leaked derived key compromises only that
  client, not the fleet.
- **AAD-bound `client_id`** in the wire envelope — captured packets with
  a swapped cleartext id fail authentication.
- **Replay protection** — inner timestamp + 16-byte replay-id, server
  dedup ring buffer (10-min TTL), `RATE_PRESSURE` fail-closed under
  eviction pressure.
- **Per-IP rate limit on `/tunnel`** (50 req/s, burst 100).
- **Server SSRF guard** rejects private/loopback/link-local/multicast/cloud-metadata IPs.
- **Per-client session cap** (default 100) and idle-session reaper
  (default 10 min).
- **Admin auth rate-limit** (8 failures / 5 min / IP).
- **cosign-signed releases** (sigstore + GitHub OIDC). Verify with
  `cosign verify-blob`; instructions in
  [docs/deployment.md](docs/deployment.md).

See [SECURITY.md](SECURITY.md) to report vulnerabilities.

---

## Important notes

- **Never share the per-client key.** Anyone with it can use your tunnel
  as if they were that client.
- **Public-internet VPS required.** The server must be reachable from
  `script.google.com`'s outbound IPs (for `appsscript`) or from the
  open internet (for `https`).
- **No local CA certificate needed.** BeaconGate tunnels raw TCP through
  SOCKS5; the browser does TLS end-to-end with the destination, and
  BeaconGate never sees the plaintext. See
  [docs/architecture.md](docs/architecture.md) §3.
- **`appsscript` is best-effort, not provably unblockable.** Re-read
  [⚠️ What this is NOT](#️-what-this-is-not) before claiming otherwise
  to users.

---

## Disclaimer

- BeaconGate is provided **for educational, testing, and research
  purposes**, AS IS, without warranty of any kind.
- Operators are responsible for **legal compliance** in their own
  jurisdiction and the jurisdictions of their users.
- Operators are responsible for **Google ToS compliance** when using
  the `appsscript` transport. Google can suspend Apps Script projects
  that match abuse patterns; this is part of the residual risk.
- Use is governed by the **Apache License 2.0** (see [LICENSE](LICENSE)).

---

## Repository layout

```
beacongate/
  cmd/                       Go binaries: client, server, admin CLI
  engine/                    shared core (transport-agnostic)
    protocol/                  versioned envelope, message types
    crypto/                    AES-256-GCM batch envelope
    config/                    JSON config loader + script_keys parser
    transport/
      https/                     direct HTTPS POST transport
      appsscript/                Apps-Script-tunneled transport with uTLS
      transporttest/             httptest-style fakes
  client/
    runtime/                   protocol roundtrip + long-poll pump
    socks/                     SOCKS5 listener
    control/                   loopback control HTTP API
  server/
    runtime/                   tunnel handler + sessions
    upstream/                  outbound dialer + SSRF guard
    policy/                    rule model + matcher + file store
    admin/                     admin HTTP API + rate-limit
  scripts/                   release-time install scripts
  apps_script/               Code.gs forwarder for the appsscript transport
  ops/                       Docker, systemd, baseline policy
  docs/                      operator + end-user guides
  mobile/                    Android (native) + iOS (reserved)
  desktop/                   (reserved)
```

---

## Development

```sh
make build           # binaries to ./bin/
make test            # unit + integration
make race            # tests with race detector
make bench           # appsscript transport benchmarks
make fuzz            # 30s fuzz against envelope decode + crypto Open
make ci              # everything CI runs
make android-apk     # release APK via Docker (no local SDK/NDK needed)
```

---

## Documentation

- [docs/architecture.md](docs/architecture.md) — system overview, glossary, diagrams
- [docs/protocol.md](docs/protocol.md) — wire protocol v1.1
- [docs/admin-api.md](docs/admin-api.md) — admin API surface
- [docs/policy.md](docs/policy.md) — policy model, rule operations, audit
- [docs/deployment.md](docs/deployment.md) — deployment reference (`https` and `appsscript`)
- [docs/troubleshooting.md](docs/troubleshooting.md) — failure-mode runbook
- [docs/uTLS-fingerprint-cadence.md](docs/uTLS-fingerprint-cadence.md) — uTLS pin and bump policy
- [mobile/android/README.md](mobile/android/README.md) — Android app build + install
- [SECURITY.md](SECURITY.md) — full residual-risk model + vulnerability reporting
- [CONTRIBUTING.md](CONTRIBUTING.md) — contributor guide

---

## License

[Apache License 2.0](LICENSE).
