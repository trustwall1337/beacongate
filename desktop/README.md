# Desktop (Phase 3)

This directory is reserved for the BeaconGate desktop product (Phase 3 in
[../docs/planning/PLAN.md](../docs/planning/PLAN.md)). It is currently a
placeholder; no implementation has been chosen yet.

## Scope

A user-facing desktop application that talks to the local BeaconGate
client process via the loopback control API ([../docs/admin-api.md](../docs/admin-api.md)
covers the server admin API; the client control API has matching
endpoints under `127.0.0.1:9091/api/...`).

See [../docs/planning/STEP-3-desktop-product.md](../docs/planning/STEP-3-desktop-product.md)
for the full product brief.

## Open architecture decisions

- Shell technology: Tauri (Rust + web), Electron, native Cocoa/GTK/Win32,
  or a SwiftUI/WPF stack per platform.
- Packaging: pkg/dmg for macOS, msi for Windows, deb/rpm/AppImage for Linux.
- Auto-update strategy.

This directory is intentionally non-Go. When work begins, add a
language-appropriate manifest (`package.json`, `Cargo.toml`, `*.xcodeproj`,
etc.) at this level. The desktop subtree is **not** part of the Go module;
nothing in `engine/`, `client/`, or `server/` should reach into here.

## Communication contract with the Go client

The desktop talks to the local Go client via:

- `GET 127.0.0.1:9091/api/status` — runtime status
- `GET 127.0.0.1:9091/api/health` — transport health
- `GET 127.0.0.1:9091/api/diagnose` — startup diagnostics

Plus the shared profile schema (see [../docs/policy.md](../docs/policy.md)
and the example configs at the repo root).

## Until then

The directory holds only this README so the planned product layout is
visible. Nothing builds from here yet.
