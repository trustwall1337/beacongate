# Changelog

All notable changes to BeaconGate are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
once it reaches `1.0.0`.

## [Unreleased] — v1.1

This release closes the censorship-evasion gap (the `appsscript` transport
delivers traffic that looks like ordinary Google Apps Script traffic to a
network observer) and bumps the wire envelope to v1.1 with per-client key
derivation, AAD-bound client_id, timestamp + replay-id, and a server-side
replay store. **Hard-cut from v1.0**; previously sealed envelopes do not
interoperate. Also closes the Phase 1 success condition: an operator can
now prepare a single bundle for an Android end-user and the user runs it
in Termux with NekoBox / v2rayNG.

### Added — Phase 1 finish (Android-via-Termux + minimal client control)

- **`make build-android`** — cross-compile `beacongate-client` for
  `linux/arm64` (Termux on Android). `CGO_ENABLED=0`, `-trimpath`,
  `-ldflags="-s -w"` for a small, static, path-stripped binary.
- **`ops/prepare-bundle.sh`** — operator-bundle script. Validates the
  config (via `beacongate-client -validate-only`), packages binary +
  config + README + `verify.sh` into a `.zip`, prints the SHA-256.
- **`beacongate-client -validate-only`** — load + validate the config,
  print structured JSON, exit 0/1. Used by `prepare-bundle.sh` and
  `verify.sh`.
- **Phase-1 client control endpoints** (`client/control`):
  - `GET /api/status` now returns the full STEP-2 status model
    (`state` enum, listen address, transport type, transport health,
    probe OK, last successful probe, last error, active profile).
  - `GET /api/events` — capped (256-entry) ring buffer of structured
    runtime events, exposed as JSON.
  - `POST /api/validate` — re-validates the loaded config + runs a
    probe round-trip. ~30 lines, no profile store; useful for the
    bundled phone-side `verify.sh`.
- **`docs/android-termux.md`** — end-user setup walkthrough (F-Droid
  Termux, `termux-wake-lock` + `Termux:Boot`, NekoBox Remote-DNS-over-
  SOCKS, IPv4-only).
- **`docs/operator-handoff-checklist.md`** — pre-flight before the
  operator sends a bundle (server reachable from a fresh network,
  policy doesn't block the friend's destinations, SHA-256 logged
  out-of-band).
- **`docs/troubleshooting.md`** — full operator runbook covering
  symptoms, first-response commands, AEAD key rotation, and per-
  failure-mode diagnostics for both transports.

### Added

- **`engine/transport/appsscript`** — new transport that tunnels every batch
  through a user-deployed Google Apps Script web app. Multi-deployment
  failover (max 2 attempts per batch), SNI rotation across multiple Google
  IPs, daily quota tracking with midnight Pacific rollover, hourly doGet
  poll for server-side counts, TLS 1.3 minimum + h2 idle ping + prewarmed
  session resumption.
- **`apps_script/Code.gs`** — the Apps Script forwarder. Implements the
  smart base64 boundary (decodes inbound, encodes outbound) so the
  BeaconGate VPS server stays binary-only across both transports. doGet
  reports daily counter + version metadata.
- **Protocol v1.1 wire envelope** (`engine/crypto`):
  - 1-byte cleartext wire-version (0x01)
  - 2-byte BE client_id length + UTF-8 client_id
  - 12-byte AEAD nonce
  - HKDF-SHA256 per-client AEAD key derivation
  - AAD-bound client_id (a captured packet posted with a swapped cleartext
    id fails authentication)
  - Inner 8-byte BE millisecond timestamp + 16-byte random replay-id
- **`engine/replay`** — server-side replay store. Two-tier per-client
  cache: response cache (60s TTL, byte-budget LRU, holds full reply
  bytes for idempotent retry) + dedup ring buffer (10min TTL, fixed cap,
  60_000 entries default). Returns RATE_PRESSURE when ring eviction
  would happen before TTL — fail closed rather than silently weakening
  replay protection.
- **Per-IP rate limit on `/tunnel`** (`server/internal/limit`) — token
  bucket at 50 req/s/IP, burst 100. Defense in depth alongside
  per-client session caps and the replay store's RATE_PRESSURE.
- **`beacongate-admin migrate-config`** — rewrites pre-v1.1 client
  configs (`transport.type=google` → `https`) in place. Idempotent;
  `--dry-run` for preview.
- **Transport acceptance criteria tests** (`engine/transport/appsscript`):
  prove the disguise invariants in CI — TCP destination is the
  configured Google IP, TLS SNI is the configured rotation entry, HTTP
  target matches `^https://script\.google\.com/macros/s/.../exec$`, no
  plaintext on the wire.
- **Client identity guarantee tests** (`server/runtime`):
  `TestSameClientIDSharesSessionCap`, `TestPolicyEvaluationIgnoresClientID`.
- **Latency-first lint** (`engine/transport/appsscript/lint_test.go`) —
  static AST check that quota-polling functions never appear in the
  request hot path.
- **Fuzz tests** for envelope decode (`engine/protocol`) and crypto
  Open (`engine/crypto`).
- **`client_config.appsscript.example.json`** — separate example file
  for appsscript-mode operators.
- **v1.1 residual-risks section** in [SECURITY.md](SECURITY.md) —
  explicit operator-facing list of what the appsscript transport does
  and does NOT defend against (traffic-pattern analysis, TLS
  fingerprinting, Google-side classifiers, URL-pattern blocking).

### Changed

- **`engine/transport/google` → `engine/transport/https`**. Renamed via
  `git mv` to preserve history. Package was a generic HTTPS POST
  transport since v1.0 — the rename matches reality. The string
  `"google"` is accepted as a deprecated alias in
  `transport.type` for one release with a deprecation log warning.
- **Application-protocol version → 1.1**. v1.0 envelopes are rejected.
- **`server.url` semantics**: in `appsscript` mode, `server.url` MUST
  be empty/omitted — the script URL is built from
  `transport.options.script_keys`. `Validate()` rejects every wrong
  combination at config load.
- **Body encoding for `appsscript`**: base64 standard alphabet **with
  padding** (`base64.StdEncoding` Go side, `Utilities.base64Encode/
  Decode` Apps Script side). The `https` transport stays binary
  (`Content-Type: application/octet-stream`); the BeaconGate VPS is
  binary-only across both transports.
- Tunnel handler enforces transport-aware client_id binding: the
  cleartext-header `client_id` (cryptographically AAD-bound) must
  match the inner JSON envelope's `client_id`.

### Removed

- **`engine/session`** — unused duplicate of the session state machine.
  Production state machines live in `server/runtime/session.go` and
  `client/runtime/sessions.go`; the `engine/session` package was dead
  code carried since v1.0.

### Security

- TLS 1.3 minimum enforced on both transports (was https-only).
- AAD-bound cleartext client_id removes pre-AEAD packet-swap attacks.
- Per-client HKDF keys remove cross-client decryption from a leaked
  derived key.
- Replay-id + timestamp + dedup cache reject in-window replay,
  out-of-window stale-timestamp packets, and pre-TTL eviction
  pressure.
- SECURITY.md placeholder email replaced with GitHub Private
  Vulnerability Reporting.

### Migration from v1.0

```sh
beacongate-admin migrate-config --file client_config.json [--dry-run]
```

Rewrites `transport.type=google` to `transport.type=https` in place
(idempotent). The deprecated `"google"` string remains accepted for
one release with a deprecation log warning; remove the alias support
in v1.2.

### Documentation

- [docs/protocol.md](docs/protocol.md) — fully revised for v1.1: new
  Version Model section (separating wire-envelope version from
  application-protocol version), Wire Envelope (v1.1) section
  describing the layout, residual identity guarantees, updated
  Implementation Traceability mapping spec sections to source files.
- [docs/deployment.md](docs/deployment.md) — split into two
  non-cross-referencing playbooks: Direct HTTPS deployment (`https`
  mode) and Apps Script deployment (`appsscript` mode), with a
  normative transport-mode matrix at the top.
- [docs/architecture.md](docs/architecture.md) — Transport status
  callout box; updated transport glossary.
- [README.md](README.md) — Transport modes table.
- [docs/planning/STEP-1-core-engine.md](docs/planning/STEP-1-core-engine.md)
  — Retrospective on the v1.0 "google transport" naming gap and how
  v1.1 closes it.
- [apps_script/README.md](apps_script/README.md) — Code.gs setup,
  why it does base64 (and why we DON'T want to "simplify" it), quota
  story.

## [v1.0]

### Added

- Initial protocol v1.0: envelope, OPEN/DATA/CLOSE/RESET/PING/PROBE,
  session lifecycle, error semantics.
- AES-256-GCM wire envelope (`engine/crypto`).
- Google-fronted HTTPS transport with optional `FrontingHost` and TLS 1.3
  minimum. *(Note: this transport was named for an aspirational
  Google-disguised role it never actually played; v1.1 renames it to
  `https` and adds a real `appsscript` transport for that role.)*
- Client SOCKS5 listener with optional username/password auth.
- Client local control API (loopback only).
- Server tunnel handler with long-poll for server-initiated data.
- Server outbound policy engine with file store and admin API.
- SSRF guard rejecting private, loopback, link-local, multicast, and
  cloud-metadata addresses by default.
- Per-client session cap and idle-session reaper.
- Admin auth rate-limit (8 failures / 5 min / IP).
- Per-message gzip compression for `DATA` payloads ≥ 256 bytes.
- Three CLIs: `beacongate-client`, `beacongate-server`, `beacongate-admin`.
- systemd unit, Dockerfile, docker-compose example.

[Unreleased]: https://github.com/trustwall1337/beacongate/compare/v1.0...HEAD
[v1.0]: https://github.com/trustwall1337/beacongate/releases/tag/v1.0
