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

### Added — v1.1.0 end-user-experience parity with Goose

This batch is the "usable v1, not POC" workstream. Eight items
shipped that close end-user-experience gaps where BeaconGate
previously felt slower or more friction-heavy than V2RayNG /
GooseRelayVPN / similar tools the friend on the phone is comparing
against.

- **`bg://` share-link + QR-code import** — operator runs
  `beacongate-admin export-link --config client.json --qr` (or
  `--qr-png file.png`); friend pastes/scans and runs
  `beacongate-client -import "<bg://...>"`. Setup goes from 10
  minutes of JSON editing to under 30 seconds. Sensitive-data
  warning printed every export.
- **Multi-profile CLI** —
  `${XDG_CONFIG_HOME}/beacongate/profiles/<name>.json`.
  `beacongate-client -profile work` vs `-profile home`.
  `-list-profiles` enumerates. Strict name validation blocks path
  traversal.
- **Account-bucket-aware endpoint selection** — `script_keys`'s
  `account` labels now drive selection: `pick()` rotates buckets
  first (so quota draw spreads across operator Google accounts),
  `pickFallback()` prefers same-bucket alternates before crossing
  accounts. Honest scope: this is the *selection* half. Goose's full
  N-workers-per-bucket parallelism (and the matching
  `idle_slots_per_bucket` knob) needs a Pump-level concurrency
  refactor and lands in v1.2.
- **`coalesce_step_ms` adaptive uplink coalescing** — pump TX
  defers up to N ms (default 0 = off; recommended 20–40) for more
  outbound frames, collapsing interactive bursts (SSH typing, REST
  polling) into single HTTP POSTs. ~80% fewer POSTs for SSH-style
  workloads. Safety cap = 5×window.
- **`upstream_proxy` (Cloudflare WARP integration)** —
  `socks5://127.0.0.1:40000` routes outbound through a local SOCKS5
  proxy. Cloudflare-protected sites stop showing captchas because
  destinations see the WARP egress IP, not the VPS's datacenter IP.
  SSRF guard still runs locally on the resolved IP.
- **Per-account quota endpoint + `-status` pretty-print** —
  `GET /api/quota` returns aggregated per-Google-account quota usage.
  `beacongate-client -status -control-addr 127.0.0.1:9091` prints
  human-readable usage bars ("alpha: 8431/20000 (42%)
  [█████╸━━━━━━] deployments=2 (2 healthy)"). Live-refresh TUI
  deferred to v1.1.1.
- **Auto-reconnect + state machine** — Pump tracks consecutive
  failures: 3 → state=Degraded; 5+ → state=Error and exponential
  backoff (3s/6s/12s/24s/30s cap). Successful tick clears counters
  and emits a `pump.reconnected` event. Tunnel survives transient
  5xx and longer outages without manual restart. Network-change
  watcher (Linux /proc/net/route) deferred to v1.2.
- **`socks_user`/`socks_pass` documentation** — already worked but
  was undocumented; README + deployment.md now cover it with a
  "set-it-when-binding-non-loopback" warning.
- **`preflight.ok` log line** — single human-readable smoke
  signal at startup ("relay healthy, AES key matches end-to-end")
  instead of split-across-multiple-slog-events.

### Honest deferrals (named so they don't slip)

- **`idle_slots_per_bucket` knob** — deferred to v1.2. Needs
  per-bucket worker pool (Pump-level concurrency refactor). v1.1.0's
  bucket-aware selection improves quota distribution but the carrier
  is still single-Roundtrip-at-a-time.
- **Zstd batch-level compression** — deferred to v1.1.1. Needs
  PROBE-based capability negotiation + bidirectional fallback tests
  for safe roll-out. v1.1.0 keeps per-message gzip ≥256B; the
  ~3× quota-economy gap vs Goose is partially mitigated by
  `coalesce_step_ms` (above), which cuts SSH-style POST counts by
  ~80%.
- **Live-refresh quota TUI** — deferred to v1.1.1. v1.1.0 ships
  one-shot `--status` only.
- **Linux network-change watcher** — deferred to v1.2.

### Added — v1.1.0 release-readiness (operator UX + supply-chain integrity)

- **uTLS Chrome 131 ClientHello fingerprinting** in the `appsscript`
  transport (`engine/transport/appsscript/utls_dial.go`). Defeats
  JA3/JA4-fingerprinting at the wire layer — every comparable
  project (Goose, MasterHttpRelayVPN) leaves this unfixed. Library
  pinned to `github.com/refraction-networking/utls v1.8.2`; profile
  pinned to `HelloChrome_131` (deterministic, not `_Auto`). Bump
  cadence in [`docs/uTLS-fingerprint-cadence.md`](docs/uTLS-fingerprint-cadence.md):
  one Chrome major per BeaconGate minor.
- **Tag-driven release pipeline** at
  [`.github/workflows/release.yml`](.github/workflows/release.yml).
  Push a `v*` tag → 6 archives (linux/macOS/windows/android) +
  SHA-256 checksums + cosign-signed checksums + GHCR image
  (linux/amd64,arm64) + cosign-signed image. All signing uses
  GitHub OIDC + sigstore; no operator key management.
- **`scripts/install.sh`** — VPS one-liner.
  `curl -fsSL .../install.sh | bash` downloads the latest release,
  **verifies the cosign signature** before unpacking (not just
  SHA-256 — bit-flip-only protection isn't threat-model-relevant for
  a censorship-evasion tool), generates an AES key, writes
  `/etc/beacongate/server_config.json`, and installs the systemd
  unit. Idempotent.
- **`make release` target** that mirrors what the release pipeline
  builds in CI — local dry-run support without pushing a tag.
- **`script_keys` accepts both shapes**
  (`engine/config/script_keys.go`): legacy comma-separated string
  AND Goose-natural array-of-objects
  `[{"id":"...","account":"..."}]`. Backward compatible.
  `migrate-config` rewrites string → array form.
- **README rewrite** — operator-first structure. Tagline leads with
  the end-user property; "What this is NOT" consent block surfaces
  the four residual risks BEFORE setup so end users in censored
  countries can give informed consent. Adds Important Notes /
  Disclaimer / Support sections. Includes the explicit "no local CA
  cert ever" line clarifying BeaconGate is unlike MasterHttpRelayVPN-
  style local-MITM designs.

### Changed

- **Breaking source-API change:**
  `engine/config.ClientTransportConfig.Options` is now
  `map[string]any` (was `map[string]string`). End-user JSON configs
  are backward compatible (a string-valued option still parses
  correctly), but anyone vendoring this struct directly needs to
  update. Use the new `OptionString(key)` helper for stdlib-equivalent
  ergonomics.
- **TLS-fingerprint residual risk closed for the default build.**
  [`SECURITY.md`](SECURITY.md) updated to reflect that the JA3/JA4
  gap previously documented as unfixed is now closed via uTLS, with
  caveats about pinning regression and future fingerprinting
  techniques.
- **`engine/transport/appsscript/fronting.go`** rewritten to use the
  uTLS dialer via `http.Transport.DialTLSContext` and a wrapper
  `net.Conn` that bridges `utls.ConnectionState` →
  `tls.ConnectionState` so HTTP/2 ALPN routing still works.
- **README.md / docs/architecture.md / docs/deployment.md /
  docs/troubleshooting.md / docs/operator-handoff-checklist.md**
  updated to reflect uTLS, cosign-verified releases, and the
  array-of-objects `script_keys` shape.

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
