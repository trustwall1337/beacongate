# BeaconGate

BeaconGate is a clean-room relay platform built around a transport-agnostic
core engine. Encrypted batches travel between a local SOCKS5 client and a
remote exit server; the exit server enforces server-side outbound policy
before dialing.

It is a **multi-language monorepo**. **Phase 1 is complete:** the Go
implementation ships the v1.1 engine, the `appsscript` censorship-
evasion transport, the Phase-1 client control surface, and the
operator-side Android-via-Termux handoff path. Desktop and mobile
workstreams are reserved as sibling subtrees for Phase 2+.

## Transport modes

| Mode | Status | What it does | Use it when |
| --- | --- | --- | --- |
| `https` | **Ships today** | Direct HTTPS POST to an operator-configured URL. Generic HTTPS, **NOT a censorship-evasion path on its own**. A network observer sees TLS to your relay's hostname. | You operate your own relay behind a CDN / your own domain fronting, or you don't need on-path-censor evasion. |
| `appsscript` | **Ships today (v1.1)** | Tunnels every batch through a user-deployed Google Apps Script web app. Wire path terminates at a real Google IP with `SNI=www.google.com` and HTTP `Host: script.google.com`. | You need network traffic that looks like ordinary Google/Apps Script traffic to a network observer. |

> **Note on the historical naming:** the v1.0 release shipped a single
> transport package named `engine/transport/google` that was actually a
> generic HTTPS POST — the name was aspirational. v1.1 renamed it to
> `engine/transport/https` (matching reality) and added a new
> `engine/transport/appsscript` package that delivers the actual
> censorship-evasion property. See
> [docs/planning/STEP-1-core-engine.md](docs/planning/STEP-1-core-engine.md)
> §"Retrospective" for the full story.

## Repository layout

```
beacongate/
  cmd/                       Go binaries: client, server, admin CLI
  engine/                    Go: shared core
    protocol/                  versioned envelope, message types
    crypto/                    AES-256-GCM batch envelope
    session/                   session state machine
    config/                    JSON config loader
    transport/                 transport abstraction
      https/                     Direct HTTPS POST transport — operator-controlled
                                   relay, NOT a censorship-evasion path on its own.
      appsscript/                Apps-Script-tunneled transport (the actual
                                   censorship-evasion path; ships in v1.1)
      transporttest/             httptest-style fakes
  client/                    Go: client side of the relay
    runtime/                   protocol roundtrip, long-poll pump
    socks/                     SOCKS5 listener (CONNECT, optional auth)
    control/                   loopback control HTTP API
  server/                    Go: server side of the relay
    runtime/                   tunnel handler + sessions
    upstream/                  outbound dialer + SSRF guard + DNS cache
    policy/                    rule model, matcher, engine, file store
    admin/                     admin HTTP API + rate-limit
  desktop/                   (Phase 3) reserved for the desktop product
  mobile/                    (Phase 4) reserved for native mobile clients.
                               Phase 1 Android end-users run the linux/arm64
                               binary in Termux — see docs/android-termux.md.
    ios/                       iOS placeholder
    android/                   Android placeholder (no native code in Phase 1)
  protocol/                  cross-language protocol home (schemas later)
  test/integration/          Go cross-package end-to-end tests
  ops/                       Docker, systemd, baseline policy
  docs/                      product docs and operator guides
    planning/                  PLAN.md and per-step roadmap
  tools/                     dev/build helpers (placeholder)
  .github/                   CI workflows + dependabot
```

The **Go module** rooted at `go.mod` covers `cmd/`, `engine/`, `client/`,
`server/`, and `test/`. `desktop/` and `mobile/` are NOT part of the Go
module; each will bring its own toolchain (Tauri/Electron/Xcode/Gradle/…).
`docs/`, `protocol/`, `ops/`, and `tools/` are language-neutral.

## Quick start

```sh
# 1. Generate a shared key
./bin/beacongate-admin gen-key

# 2. Edit client_config.json and server_config.json (see *.example.json).

# 3. Run the server (locally or via systemd / docker compose)
./bin/beacongate-server -config server_config.json

# 4. Run the client
./bin/beacongate-client -config client_config.json -control-addr 127.0.0.1:9091

# 5. Send traffic through it
curl -x socks5h://127.0.0.1:1080 https://example.com
```

See [docs/deployment.md](docs/deployment.md) for the full setup, including
TLS termination, systemd, Docker Compose, and recovery tips. To prepare
a bundle for an Android friend running Termux + NekoBox/v2rayNG, see
[docs/operator-handoff-checklist.md](docs/operator-handoff-checklist.md)
and [docs/android-termux.md](docs/android-termux.md).

## Documentation

- [docs/architecture.md](docs/architecture.md) — system overview, glossary, diagrams (start here)
- [docs/protocol.md](docs/protocol.md) — wire protocol v1.1
- [docs/admin-api.md](docs/admin-api.md) — admin API surface
- [docs/policy.md](docs/policy.md) — policy model, rule operations, audit
- [docs/deployment.md](docs/deployment.md) — operator deployment guide
- [docs/troubleshooting.md](docs/troubleshooting.md) — failure-mode runbook
- [docs/operator-handoff-checklist.md](docs/operator-handoff-checklist.md) — pre-handoff verification
- [docs/android-termux.md](docs/android-termux.md) — end-user setup on Android (Termux + NekoBox)
- [SECURITY.md](SECURITY.md) — vulnerability reporting
- [CONTRIBUTING.md](CONTRIBUTING.md) — contributor guide

## Development (Go subtree)

```sh
make build         # binaries to ./bin/
make build-android # cross-compile beacongate-client for linux/arm64 (Termux)
make test          # unit + integration
make race          # tests with race detector
make bench         # appsscript transport benchmarks
make fuzz          # 30s fuzz against envelope decode + crypto Open
make vet           # go vet ./...
make fmt           # gofmt -w .
make lint          # golangci-lint run
make ci            # everything CI runs
```

Each non-Go subtree (when added) gets its own build commands; the
top-level Makefile delegates to per-subtree Makefiles.

## Security defaults

- TLS 1.3 minimum on **both** transports (`https` and `appsscript`).
- AES-256-GCM authenticated encryption on every batch, with **per-client
  HKDF-derived keys** (a leaked derived key compromises only that
  client, not the whole fleet).
- **AAD-bound `client_id`** in the wire envelope — a captured packet
  posted with a swapped cleartext id fails authentication.
- **Replay protection** — inner timestamp + 16-byte replay-id, with a
  server-side dedup ring buffer (10-min TTL) and `RATE_PRESSURE`
  fail-closed under eviction pressure.
- **Per-IP rate limit on `/tunnel`** (50 req/s, burst 100) layered on
  top of per-client session caps.
- Server SSRF guard rejects private/loopback/link-local/multicast/cloud-metadata IPs.
- Per-client session cap (default 100) and idle-session reaper (default 10 min).
- Admin auth rate-limit (8 failures / 5 min / IP).
- Per-message gzip compression for `DATA` payloads ≥ 256 bytes.

See [SECURITY.md](SECURITY.md) to report vulnerabilities.

## License

[Apache License 2.0](LICENSE).
