# BeaconGate

**BeaconGate** is a security-conscious, transport-agnostic SOCKS5
tunnel that routes your traffic through Google so a network observer
in a censored country only sees TLS to a Google IP. Bytes are
AES-256-GCM sealed end-to-end with per-client HKDF-derived keys;
Google never holds the key and never sees plaintext. The TLS
handshake itself uses
[uTLS](https://github.com/refraction-networking/utls) to mimic a
Chrome 131 ClientHello, reducing naive JA3/JA4 distinguishability —
this is not a guarantee against active probing, traffic analysis,
Google-side classification, or future fingerprinting methods.
Built on a transport-agnostic engine with replay protection,
AAD-bound client identity, and server-side outbound policy
enforcement.

---

## ⚠️ What this is NOT

Before you install BeaconGate, understand what it does NOT defend
against. End users in censored countries deserve informed consent.

- **Traffic-pattern analysis.** A sophisticated observer with full
  packet capture can fingerprint long-poll cadence, payload-size
  distribution, and per-batch timing — even though the bytes are
  encrypted. They may infer *that* you're tunneling, even if not *what*.
- **Google-side classifiers.** Google itself can flag and disable Apps
  Script projects that match abuse patterns. We have no control over
  this.
- **URL-pattern blocking.** A censor could block
  `script.google.com/macros/s/.../exec` paths specifically (rather
  than `script.google.com` wholesale), with broad collateral damage to
  legitimate Apps Script users.
- **TLS-fingerprint analysis (with caveats).** v1.1.0 closes the
  default-build gap with uTLS. But: pinning regression, stdlib
  fallback, or new fingerprinting techniques could re-open it.
  The bump cadence in [docs/uTLS-fingerprint-cadence.md](docs/uTLS-fingerprint-cadence.md) is the answer.

**BeaconGate raises the cost of blocking. It does not make blocking
impossible.** If you need a tool that survives a state-level adversary,
BeaconGate is not it; consider Tor with bridges. If you're OK with the
above, proceed to setup.

See [SECURITY.md](SECURITY.md) for the full residual-risk model.

---

## How it works

```
[ your apps ]  ←SOCKS5→  [ BeaconGate client ]  ←Apps Script tunnel→  [ BeaconGate server ]  ←TCP→  [ destinations ]
                          (laptop or phone)        (looks like Google)    (your VPS)
```

Your local apps point their SOCKS5 setting at the BeaconGate client
(default `127.0.0.1:1080`). The client wraps each batch in an
AES-256-GCM-sealed envelope and POSTs it to a Google Apps Script web
app you've deployed under your own Google account. The Apps Script
forwards the bytes to your VPS, which decrypts, runs server-side
outbound policy (no torrents, no SSRF, no metadata IPs), dials the
real destination, and pipes bytes back the same way.

A network observer between you and Google sees what looks like a
Chrome browser making ordinary HTTPS requests to
`script.google.com`. They see neither the destination, nor the
plaintext, nor the BeaconGate server's IP. See
[docs/architecture.md](docs/architecture.md) for the full picture.

---

## Transport modes

| Mode | What it does | Use it when |
| --- | --- | --- |
| **`appsscript`** | Tunnels every batch through a user-deployed Google Apps Script web app. Wire path terminates at a real Google IP with `SNI=www.google.com` (uTLS-fingerprinted as Chrome 131) and HTTP `Host: script.google.com`. | You need network traffic that looks like ordinary Google traffic to a network observer. **This is the censorship-evasion path.** |
| `https` | Direct HTTPS POST to an operator-configured URL. Generic HTTPS, **NOT a censorship-evasion path on its own**. A network observer sees TLS to your relay's hostname. | You operate your own relay behind a CDN / your own domain fronting, or you don't need on-path-censor evasion. |

> **Historical naming:** v1.0 shipped a single transport package
> named `engine/transport/google` that was actually a generic HTTPS
> POST — the name was aspirational. v1.1 renamed it to
> `engine/transport/https` (matching reality) and added a new
> `engine/transport/appsscript` package that delivers the actual
> censorship-evasion property. See
> [docs/planning/STEP-1-core-engine.md](docs/planning/STEP-1-core-engine.md)
> §"Retrospective" for the full story.

---

## Quick start

> **First time setting this up?** The terse summary below assumes you already know your way around a VPS. If you're starting from a brand-new server with no idea where to begin, read [**docs/getting-started.md**](docs/getting-started.md) instead — it's a complete zero-to-tunnel walkthrough with every step (rent a Hetzner VPS → SSH → install Go → build → server config → firewall on both layers → Apps Script deployment → operator dry-run → Android bundle → end-user phone setup), every gotcha I've hit in the field, and an honest "known limitations" section before you ship to a real user.

### Server side (your VPS, outside the censored network)

```sh
# One-liner installer (Linux VPS):
curl -fsSL https://raw.githubusercontent.com/trustwall1337/beacongate/main/scripts/install.sh | bash

# OR manually:
# 1. Download release for your VPS arch from https://github.com/trustwall1337/beacongate/releases
# 2. Verify cosign signature (signed by GitHub Actions OIDC)
# 3. Generate an AES key:
./bin/beacongate-admin gen-key
# 4. Edit /etc/beacongate/server_config.json with the key, set up systemd
# 5. Open port 8080 to script.google.com (or your reverse proxy)
```

### Apps Script side (your Google account)

1. Open <https://script.google.com/> → New project → paste
   [apps_script/Code.gs](apps_script/Code.gs) → edit `RELAY_URL` to
   point at your VPS.
2. Deploy → New deployment → Web app → Execute as Me → Anyone.
3. Copy the **Deployment ID** (NOT the Script ID).

### Client side (your laptop or your friend's Android)

**Recommended path — share-link / QR code:**

```sh
# On the operator's machine, after configuring client_config.json:
beacongate-admin export-link --config client_config.json --qr
# prints: bg://config?d=... + a Unicode-block QR
# also: --qr-png /path/to/qr.png to write a PNG file
```

The friend pastes the link (or scans the QR with their phone camera):

```sh
beacongate-client -import "bg://config?d=..." -config client_config.json
# decodes the link, writes client_config.json (mode 0600), confirms before overwriting
beacongate-client -config client_config.json -control-addr 127.0.0.1:9091
```

> ⚠️ The link contains the AES key. Treat it like a password. Send
> over end-to-end-encrypted channels only (Signal, in person, etc.).

**Manual path — edit JSON directly:**

```sh
# Edit client_config.json with the AES key + Deployment ID.
# (See client_config.appsscript.example.json for the shape.)
beacongate-client -config client_config.json -control-addr 127.0.0.1:9091
curl -x socks5h://127.0.0.1:1080 https://example.com
```

For the **Android end-user flow** (Termux + NekoBox/v2rayNG), see
[docs/operator-handoff-checklist.md](docs/operator-handoff-checklist.md)
on the operator side and [docs/android-termux.md](docs/android-termux.md)
on the user side.

For the **full server deployment** with reverse-proxy TLS, systemd, and
Docker Compose, see [docs/deployment.md](docs/deployment.md).

---

## Configuration

`server_config.json` and `client_config.json` are JSON files. Examples
live in this repo:

- [server_config.example.json](server_config.example.json)
- [client_config.example.json](client_config.example.json) — `https` mode
- [client_config.appsscript.example.json](client_config.appsscript.example.json) — `appsscript` mode

`script_keys` accepts both shapes (the structured array-of-objects
form, or a legacy comma-separated string):

```json
"script_keys": [
  {"id": "DEPLOYMENT_ID_1", "account": "alpha-account"},
  {"id": "DEPLOYMENT_ID_2", "account": "beta-account"}
]
```

```json
"script_keys": "DEPLOYMENT_ID_1,DEPLOYMENT_ID_2"
```

Migrate older configs with `beacongate-admin migrate-config --file
client_config.json`.

### Adaptive uplink coalescing (`coalesce_step_ms`)

For interactive workloads (SSH typing, IRC, REST polling), the
default fires one HTTP POST per outbound frame — a chatty SSH session
can drain a single Google account's daily Apps Script quota in under
an hour. Setting `coalesce_step_ms` to a positive value makes the pump
wait that many milliseconds for additional outbound frames before
firing the request, and resets the timer on each new frame
(adaptive). Trades latency for ~80% fewer POSTs on bursty workloads.

```json
{
  ...,
  "coalesce_step_ms": 30
}
```

- `0` (default) — off, every frame fires immediately.
- `20–40` — recommended starting range; 30ms is a sensible default.
- Max `200` — hard cap; values above hurt perceived responsiveness.

A safety cap of 5× the window prevents a steady stream of frames
from deferring the flush indefinitely.

### SOCKS5 username/password authentication (optional)

The local SOCKS5 listener supports RFC 1929 username/password auth.
Empty username = no auth (default), suitable for single-user laptops
where `127.0.0.1` is already a trust boundary. **Set both
`socks.username` and `socks.password` if you bind the listener to a
non-loopback address (LAN sharing) — without auth, anyone on the LAN
can drain your daily Apps Script quota.**

```json
{
  "listen_addr": "0.0.0.0:1080",
  "socks": {
    "username": "alice",
    "password": "long-random-shared-secret"
  },
  ...
}
```

Connect with `curl -x socks5://alice:long-random-shared-secret@host:1080 ...`.

---

## Common mistakes (and how to recognize them)

- **Pasted the Script ID instead of the Deployment ID.** They look
  similar (long base64 strings) but they're different. The Apps
  Script console's deployments page shows the Deployment ID — that's
  what goes in `script_keys`.
- **Edited `Code.gs` and forgot to redeploy.** Saving the file does
  NOT update what your clients see. Every change requires
  Deploy → New deployment.
- **Set `server.url` in `appsscript` mode.** The loader rejects this
  with a clear error; the script URL is constructed from
  `script_keys`, so `server.url` MUST be empty.
- **Tunnel pauses on Android after a few minutes.** Termux gets
  killed by Android's battery optimizer. Run `termux-wake-lock`
  every session. See [docs/android-termux.md](docs/android-termux.md)
  §"Keep it running."

---

## Troubleshooting

The full failure-mode runbook is in
[docs/troubleshooting.md](docs/troubleshooting.md). Quick reference:

| Symptom | First step |
| --- | --- |
| `auth failed` (HTTP 401) | Server and client AEAD keys diverged. Regenerate and roll. |
| `transport.healthy=false` | Network unreachable (https) or Apps Script deployment broken (appsscript). |
| `RESET POLICY_DENIED` | Server policy is blocking your destination. Add an allow rule. |
| All deployments quota-blacklisted (appsscript) | Wait until midnight Pacific or add more Google accounts to `script_keys`. |
| Tunnel verifies on laptop but not phone | Termux:Boot + `termux-wake-lock` not configured. |

---

## Multiple deployments, multiple accounts

Apps Script enforces ~20K UrlFetchApp invocations/day per Google
account. Multi-deployment failover is the answer:

1. Deploy `Code.gs` in additional Google accounts.
2. Add their Deployment IDs to `script_keys` with distinct `account`
   labels. The client round-robins across them and fails over on
   per-deployment 403/quota errors (max two attempts per batch).
3. Quota usage is reported per account in the client's diagnostic
   output.

---

## Security defaults

- **TLS 1.3 minimum** on both transports (`https` and `appsscript`).
- **uTLS Chrome 131 ClientHello** on `appsscript` — mimics a real
  Chrome browser's handshake to reduce naive JA3/JA4
  distinguishability. **Not a guarantee against active probing,
  traffic analysis, Google-side classification, or future
  fingerprinting methods.** Pin tracked in
  [docs/uTLS-fingerprint-cadence.md](docs/uTLS-fingerprint-cadence.md).
- **AES-256-GCM authenticated encryption** on every batch, with
  **per-client HKDF-derived keys** (a leaked derived key compromises
  only that client, not the whole fleet).
- **AAD-bound `client_id`** in the wire envelope — a captured packet
  posted with a swapped cleartext id fails authentication.
- **Replay protection** — inner timestamp + 16-byte replay-id, with a
  server-side dedup ring buffer (10-min TTL) and `RATE_PRESSURE`
  fail-closed under eviction pressure.
- **Per-IP rate limit on `/tunnel`** (50 req/s, burst 100).
- **Server SSRF guard** rejects private/loopback/link-local/multicast/cloud-metadata IPs.
- **Per-client session cap** (default 100) and idle-session reaper
  (default 10 min).
- **Admin auth rate-limit** (8 failures / 5 min / IP).
- **cosign-signed releases** (sigstore + GitHub OIDC) — verify with
  `cosign verify-blob`. See [docs/deployment.md](docs/deployment.md).

See [SECURITY.md](SECURITY.md) to report vulnerabilities.

---

## Support this project

If BeaconGate is useful to you, please ⭐ the repo on GitHub. Issues
and PRs welcome — see [CONTRIBUTING.md](CONTRIBUTING.md).

<!--
Optional crypto donation block — uncomment and fill in if/when applicable.

**Donate:**
- BTC: [your-btc-address]
- ETH: [your-eth-address]
- USDT: [your-usdt-address]
-->

PRs especially welcome for:

- Additional transport implementations (WebSocket, QUIC, Cloudflare
  Workers, Fastly Compute@Edge — anything that can carry opaque
  encrypted batches).
- Native desktop / mobile clients (`desktop/`, `mobile/` — reserved
  subtrees).

---

## Important Notes

- **Never share the AES key.** Anyone with this key can use your
  tunnel/VPS as if they were you. The shared-key model is the entire
  authentication boundary.
- **Public-internet VPS required.** The BeaconGate server must be
  reachable from `script.google.com`'s outbound IPs (for `appsscript`)
  or from the open internet (for `https`).
- **Apps Script quota: ~20K calls/day per Google account.** Resets at
  midnight Pacific (10:30 AM Iran time). Plan around the boundary; add
  more Google accounts to `script_keys` to extend the daily ceiling.
- **You do NOT need to install a local CA certificate.** BeaconGate
  tunnels raw TCP through SOCKS5; your browser does TLS end-to-end
  with the destination, and BeaconGate never sees your plaintext. If
  you've used MasterHttpRelayVPN-style designs, that project's CA-cert
  step does NOT apply here — it's a different architecture. See
  [docs/architecture.md](docs/architecture.md) §3 for why the
  SOCKS5+raw-TCP model means no MITM is ever needed.
- **The `appsscript` transport is best-effort, not provably
  unblockable.** Re-read [⚠️ What this is NOT](#️-what-this-is-not)
  above. Don't sell users on a property the system doesn't deliver.
  Different design (transport-agnostic engine, server-side policy,
  per-client keys, replay protection, uTLS), same operator-friendly
  spirit.

---

## Disclaimer

- BeaconGate is provided **for educational, testing, and research
  purposes**.
- Software is provided **AS IS**, without warranty of any kind. Use
  at your own risk; the authors and contributors disclaim liability
  for any damages or legal consequences arising from its use.
- Operators are responsible for **legal compliance** in their own
  jurisdiction and the jurisdictions of their users. Some uses of
  censorship-evasion tools are illegal in some places; that's a
  decision the operator and end user must make for themselves.
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
      https/                     direct HTTPS POST transport (operator-controlled)
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
  desktop/                   (Phase 3) reserved
  mobile/                    (Phase 4) reserved — Phase 1 Android uses Termux
```

---

## Development

```sh
make build           # binaries to ./bin/
make build-android   # cross-compile beacongate-client for linux/arm64 (Termux)
make test            # unit + integration
make race            # tests with race detector
make bench           # appsscript transport benchmarks
make fuzz            # 30s fuzz against envelope decode + crypto Open
make ci              # everything CI runs
```

---

## Documentation

- 👉 **[docs/getting-started.md](docs/getting-started.md)** — *start here if it's your first time*: complete zero-to-tunnel walkthrough from a fresh VPS to your friend's phone, with every gotcha and honest known-limitations.
- [docs/architecture.md](docs/architecture.md) — system overview, glossary, diagrams
- [docs/protocol.md](docs/protocol.md) — wire protocol v1.1
- [docs/admin-api.md](docs/admin-api.md) — admin API surface
- [docs/policy.md](docs/policy.md) — policy model, rule operations, audit
- [docs/deployment.md](docs/deployment.md) — terse two-playbook deployment reference (`https` and `appsscript`)
- [docs/troubleshooting.md](docs/troubleshooting.md) — failure-mode runbook
- [docs/operator-handoff-checklist.md](docs/operator-handoff-checklist.md) — pre-handoff verification
- [docs/android-termux.md](docs/android-termux.md) — end-user setup on Android (Termux + NekoBox)
- [docs/uTLS-fingerprint-cadence.md](docs/uTLS-fingerprint-cadence.md) — uTLS pin and bump policy
- [SECURITY.md](SECURITY.md) — full residual-risk model + vulnerability reporting
- [CONTRIBUTING.md](CONTRIBUTING.md) — contributor guide

---

## License

[Apache License 2.0](LICENSE).
