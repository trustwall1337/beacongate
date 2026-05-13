# Desktop (reserved)

Reserved for a user-facing desktop application. No implementation yet.

## Scope

The desktop app would wrap the local `beacongate-client` process,
exposing connect/disconnect, transport status, and quota usage through
a GUI. It would talk to the client over the loopback control API
(`127.0.0.1:9091/api/…`) rather than embedding the Go core.

## Open architecture decisions

- Shell technology — Tauri (Rust + web), Electron, native Cocoa / GTK /
  Win32, or a SwiftUI / WPF stack per platform.
- Packaging — `pkg`/`dmg` (macOS), `msi` (Windows), `deb`/`rpm`/
  AppImage (Linux).
- Auto-update strategy.

This directory is intentionally non-Go. When work begins, add a
language-appropriate manifest (`package.json`, `Cargo.toml`,
`*.xcodeproj`, …) at this level. The desktop subtree is **not** part
of the Go module; nothing in `engine/`, `client/`, or `server/` should
reach into here.

## Communication contract with the Go client

The desktop talks to the local Go client via:

- `GET 127.0.0.1:9091/api/status` — runtime status
- `GET 127.0.0.1:9091/api/health` — transport health
- `GET 127.0.0.1:9091/api/diagnose` — startup diagnostics

See [../docs/admin-api.md](../docs/admin-api.md) for the server admin
API surface; the client control API mirrors its style under
`127.0.0.1:9091/api/...`.
