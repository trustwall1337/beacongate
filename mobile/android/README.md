# BeaconGate Android

Native Android client for BeaconGate. Replaces the Phase 1 Termux + NekoBox
flow (`docs/android-termux.md`) with a one-tap experience for non-technical
friends.

## Architecture

The Go client (existing `client/runtime` + `engine/*` packages) is exposed
to Kotlin via `gomobile bind`. The Android app wraps it with:

1. **`BeaconVpnService`** — Android system VPN service that captures all
   phone traffic into a TUN device.
2. **tun2socks** (Go library, bundled in the same `.aar`) — translates raw
   IP packets from TUN into SOCKS5 connections to the local listener.
3. **gomobile facade** (`mobile/bindings/`) — exposes `ImportConfig`,
   `StartTunnel`, `StopTunnel`, `Status`, `GetStats`, `Version`,
   `SetLogSink` to Kotlin.
4. **Kotlin UI** — single-screen `MainActivity` with import (Drive link or
   file picker), connect/disconnect, and status indicator.

Full plan: see the project plan file (approved 2026-05-10).

## Building

The build is **fully Dockerized** — no local Android SDK or NDK needed,
only Docker. Phone testing uses the host's `adb`.

```sh
# One-time: build the Docker image with SDK + NDK + Gradle (~10 min).
make android-build-image

# Each iteration:
make android-aar     # produces bin/beacongate.aar  (via gomobile bind)
make android-apk     # produces a release APK       (via Gradle)

# Install on a phone connected via USB (adb installed on host):
adb install mobile/android/app/build/outputs/apk/release/app-arm64-v8a-release.apk
```

`make android-clean` drops the Docker volumes used as the Go module +
Gradle home cache. Only needed if a build wedges or after a major
toolchain bump.

## Local development (optional)

If you prefer Android Studio:
1. `brew install --cask android-studio`
2. Open `mobile/android/` as a project. Studio will resolve the Gradle
   wrapper (8.7) and pull dependencies on first sync.
3. Build the AAR via the Docker target (`make android-aar`) before
   running — the Gradle build expects `app/libs/beacongate.aar` to exist.

The Docker path is the canonical one; Android Studio is optional and
only useful for UI iteration / on-device debugging.

## Module layout

```
mobile/android/
├── settings.gradle.kts        # single-module project (:app)
├── build.gradle.kts           # plugin versions
├── gradle.properties          # JVM args, AndroidX flag
├── gradle/wrapper/            # gradle-wrapper config
└── app/
    ├── build.gradle.kts       # Android config (sdk/abi/proguard)
    ├── proguard-rules.pro     # keep gomobile classes
    ├── libs/                  # vendored .aar (gitignored)
    └── src/main/
        ├── AndroidManifest.xml
        ├── java/com/beacongate/
        │   └── MainActivity.kt   # Step 3 stub
        └── res/                # strings, themes, layout
```

Subsequent steps (4–7) add `CredentialStore`, `ConfigImport`,
`ConnectionState`, `MainViewModel`, `BeaconVpnService` under
`java/com/beacongate/` without touching the Gradle config.
