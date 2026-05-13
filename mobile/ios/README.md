# BeaconGate iOS (reserved)

Reserved for an iOS client. No implementation yet.

When work begins, the Xcode project (`*.xcodeproj`, `Package.swift`,
or workspace) lives at this level. The Go core in `engine/` is
available via `gomobile bind -target=ios`; the architecture decision
(gomobile vs. a Swift reimplementation vs. a Network Extension that
calls into a Swift port) is open.
