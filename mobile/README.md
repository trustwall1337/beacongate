# Mobile

BeaconGate's mobile subtree. Each platform's app is self-contained
under its own directory.

## Subtree layout

- [`android/`](android/README.md) — native Android app. Shipped: Kotlin
  UI + system VPN service, with the Go client core (`engine/*`,
  `client/runtime`) exposed via `gomobile bind`.
- [`bindings/`](bindings/) — the gomobile-bind facade
  (`ImportConfig`, `StartTunnel`, `StopTunnel`, …) consumed by the
  Android app. The same package is the integration point for any
  future mobile platform that uses gomobile.
- [`ios/`](ios/README.md) — reserved for an iOS app (language and
  architecture TBD).

## Communication contract

Same protocol as desktop and the Go client: encrypted batches over an
allowed transport, defined in [../docs/protocol.md](../docs/protocol.md).
Mobile platforms may need additional transport implementations (e.g.
background-friendly fronting); those live alongside the existing
transports in `engine/transport/` if Go-shared, or inside this subtree
if platform-native.
