# BeaconGate

BeaconGate is a clean-room relay platform built around a transport-agnostic
core engine. Encrypted batches travel between a local SOCKS5 client and a
remote exit server; the exit server enforces server-side outbound policy
before dialing.

It is a **multi-language monorepo**. The current Go implementation covers
phases 1 and 2 (engine + control API). Desktop and mobile workstreams are
reserved as sibling subtrees and will be added in their own languages.

## Transport modes

| Mode | Status | What it does | Use it when |
| --- | --- | --- | --- |
| `https` (currently named `google` in source — being renamed in v1.1) | **Ships today** | Direct HTTPS POST to an operator-configured URL. Generic HTTPS, **NOT a censorship-evasion path**. A network observer sees TLS to your relay's hostname. | You operate your own relay behind a CDN / your own domain fronting, or you don't need on-path-censor evasion. |
| `appsscript` | **Lands in v1.1** | Tunnels every batch through a user-deployed Google Apps Script web app. Wire path terminates at a real Google IP with `SNI=www.google.com` and HTTP `Host: script.google.com`. | You need network traffic that looks like ordinary Google/Apps Script traffic to a network observer. |

> **Note on the historical naming:** the current `engine/transport/google`
> package was named for the *intended* Google-facing role, but in `master`
> it is just a generic HTTPS transport. The v1.1 release renames the
> package to `https` and adds a new `engine/transport/appsscript` package
> that delivers the actual censorship-evasion property. See
> [docs/architecture.md](docs/architecture.md) §1 for the full story and
> [docs/planning/STEP-1-core-engine.md](docs/planning/STEP-1-core-engine.md)
> §"Retrospective" for why this gap exists.

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
      google/                    Direct HTTPS POST transport (renamed `https` in v1.1).
                                   NOT a Google-disguised path despite the package name.
      appsscript/                (v1.1) Apps-Script-tunneled transport (the actual
                                   censorship-evasion path)
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
  mobile/                    (Phase 4) reserved for mobile clients
    ios/                       iOS placeholder
    android/                   Android placeholder
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
TLS termination, systemd, Docker Compose, and recovery tips.

## Documentation

- [docs/architecture.md](docs/architecture.md) — system overview, glossary, diagrams (start here)
- [docs/protocol.md](docs/protocol.md) — wire protocol v1.0
- [docs/admin-api.md](docs/admin-api.md) — admin API surface
- [docs/policy.md](docs/policy.md) — policy model and evaluation order
- [docs/deployment.md](docs/deployment.md) — operator guide
- [SECURITY.md](SECURITY.md) — vulnerability reporting
- [CONTRIBUTING.md](CONTRIBUTING.md) — contributor guide

## Development (Go subtree)

```sh
make build        # binaries to ./bin/
make test         # unit + integration
make race         # tests with race detector
make vet          # go vet ./...
make fmt          # gofmt -w .
make lint         # golangci-lint run
make ci           # everything CI runs
```

Each non-Go subtree (when added) gets its own build commands; the
top-level Makefile delegates to per-subtree Makefiles.

## Security defaults

- TLS 1.3 minimum on the client transport.
- AES-256-GCM authenticated encryption on every batch.
- Server SSRF guard rejects private/loopback/link-local/multicast/cloud-metadata IPs.
- Per-client session cap (default 100) and idle-session reaper (default 10 min).
- Admin auth rate-limit (8 failures / 5 min / IP).
- Per-message gzip compression for `DATA` payloads ≥ 256 bytes.

See [SECURITY.md](SECURITY.md) to report vulnerabilities.

## License

[Apache License 2.0](LICENSE).
