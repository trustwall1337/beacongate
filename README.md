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

## Support this project

If BeaconGate is useful to you, please ⭐ the repo on GitHub — it
helps more people find the project. Issues and PRs are welcome; see
[CONTRIBUTING.md](CONTRIBUTING.md).

PRs especially welcome for:

- Additional transport implementations (WebSocket, QUIC, Cloudflare
  Workers, Fastly Compute@Edge — anything that can carry opaque
  encrypted batches).
- iOS client (`mobile/ios/` is reserved).
- Desktop wrapper (`desktop/` is reserved).

---

## How it works

Local apps point their SOCKS5 setting at the BeaconGate client. The
client wraps each batch in an AES-256-GCM sealed envelope and posts it
to a Google Apps Script web app deployed under your own Google
account. The Apps Script forwards the bytes to your VPS, which
decrypts, applies server-side outbound policy, dials the destination,
and pipes the response back the same way. A network observer between
you and Google sees what looks like Chrome making ordinary HTTPS
requests to `script.google.com` — not the destination, not the
plaintext, not the VPS's IP.

For the full picture (diagrams, glossary, end-to-end data flow), see
[docs/architecture.md](docs/architecture.md).

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
transport options. This JSON is what you hand to the end user — they
import it into the BeaconGate app and they're done.

> The JSON contains an AES key. Treat it like a password — transfer over
> Signal, scp, or in person; never over plaintext channels.

### 4. Client

#### Android (native app)

The Android app is a system VPN service that captures all phone traffic
into the tunnel. No Termux, no manual SOCKS5 wiring on the phone.

1. Build the release APK on your laptop:
   ```sh
   make android-build-image   # one-time, ~10 min
   make android-apk           # produces a release APK (~15 MB)
   ```
2. Install the APK on the end user's phone, then open BeaconGate.
3. Import `alice.json` (Drive link or file picker) and tap **Connect**.
   Accept the system VPN dialog the first time.

Full build + install walkthrough in
[mobile/android/README.md](mobile/android/README.md). For the legacy
Termux path, see [docs/android-termux.md](docs/android-termux.md).

#### Laptop (macOS / Linux / Windows)

Download the matching release tarball from
[GitHub Releases](https://github.com/trustwall1337/beacongate/releases),
verify the cosign signature (command in the release notes), then:

```sh
beacongate-client -config alice.json -control-addr 127.0.0.1:9091
```

Point any SOCKS5-capable client at `127.0.0.1:1080`.

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

The options below all live in the **client** config JSON — either the
file `beacongate-admin add-client` produces, or your own
`client_config.json` for a laptop client. The full shape is in
[client_config.appsscript.example.json](client_config.appsscript.example.json)
(`appsscript` mode) or [client_config.example.json](client_config.example.json)
(`https` mode). Server-side fields are in
[server_config.example.json](server_config.example.json).

### `script_keys` — Apps Script deployment IDs

`script_keys` lives under `transport.options` and lists the Google
Apps Script deployments the client will route through. Use one entry
per deployment; the client round-robins across them and fails over on
per-deployment errors. Adding more entries from additional Google
accounts extends the daily quota (see "Multiple deployments" below).

Two formats are accepted; prefer the array form:

```json
"transport": {
  "type": "appsscript",
  "options": {
    "script_keys": [
      {"id": "AKfyc...DEPLOYMENT_ID_1", "account": "alpha-account"},
      {"id": "AKfyc...DEPLOYMENT_ID_2", "account": "beta-account"}
    ]
  }
}
```

A legacy comma-separated string is still parsed for backward
compatibility:

```json
"script_keys": "AKfyc...DEPLOYMENT_ID_1,AKfyc...DEPLOYMENT_ID_2"
```

To migrate an older config in place:
`beacongate-admin migrate-config --file client_config.json`.

### `coalesce_step_ms` — adaptive uplink coalescing (optional)

Top-level field in the client config. **Leave at `0` (the default)
unless you're hitting the per-account Apps Script quota.** When set
above zero, the pump waits this many milliseconds for additional
outbound frames before firing a POST, batching interactive bursts
(SSH typing, REST polling) into single requests. Trades latency for
~80% fewer POSTs.

```json
{
  "client_id": "alice",
  "coalesce_step_ms": 0
}
```

- `0` (default) — off, every TX fires immediately. Lowest latency.
- `20–40` — opt-in for quota-bound deployments. `30` is a sensible
  starting point.
- `200` — hard cap. Values above hurt perceived responsiveness.

### SOCKS5 username/password (optional)

Top-level `socks` block in the client config. Empty username = no
auth (default), which is safe when `listen_addr` is loopback. **Set
both `socks.username` and `socks.password` whenever you bind to a
non-loopback address**, otherwise anyone on the LAN can drain your
quota.

```json
{
  "listen_addr": "0.0.0.0:1080",
  "socks": {
    "username": "alice",
    "password": "long-random-shared-secret"
  }
}
```

Connect with
`curl -x socks5://alice:long-random-shared-secret@host:1080 …`.

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
