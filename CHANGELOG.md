# Changelog

All notable changes to BeaconGate are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
once it reaches `1.0.0`.

## [Unreleased]

### Added

- Initial protocol v1.0: envelope, OPEN/DATA/CLOSE/RESET/PING/PROBE,
  session lifecycle, error semantics.
- AES-256-GCM wire envelope (`engine/crypto`).
- Google-fronted HTTPS transport with optional `FrontingHost` and TLS 1.3
  minimum.
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

### Documentation

- [docs/protocol.md](docs/protocol.md) — wire protocol v1.0.
- [docs/admin-api.md](docs/admin-api.md) — admin API surface.
- [docs/policy.md](docs/policy.md) — policy model.
- [docs/deployment.md](docs/deployment.md) — operator guide.

[Unreleased]: https://github.com/trustwall1337/beacongate
