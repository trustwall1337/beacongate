# Mobile (Phase 4)

This directory is reserved for BeaconGate mobile clients (Phase 4 in
[../docs/planning/PLAN.md](../docs/planning/PLAN.md)). Mobile is in scope
but **strategy first** — the implementation language and architecture
are not yet decided.

See [../docs/planning/STEP-4-mobile-strategy.md](../docs/planning/STEP-4-mobile-strategy.md)
for the program brief.

## Subtree layout (current placeholders)

- `ios/` — reserved for an iOS app (Xcode project, Swift/SwiftUI).
- `android/` — reserved for an Android app (Gradle, Kotlin).

Whether mobile shares the same engine as the Go client (via gomobile,
ffi, or a clean reimplementation per platform) is an open architectural
question. The current planning doc requires a written decision before
any implementation work starts.

## Communication contract

Same protocol as desktop and the Go client: encrypted batches over an
allowed transport, defined in [../docs/protocol.md](../docs/protocol.md).
Mobile platforms may need additional transport implementations
(e.g. background-friendly fronting) which would live alongside the
existing transports in `engine/transport/` if Go-shared, or inside this
mobile/ subtree if platform-native.

## Until then

These directories are placeholders so the planned monorepo layout is
visible. Nothing builds from here yet.
