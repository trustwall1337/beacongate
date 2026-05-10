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
make android-aar         # produces bin/beacongate.aar     (via gomobile bind)
make android-apk         # produces a release APK (~15 MB, unsigned)
make android-apk-debug   # produces a debug-signed APK (~20 MB, installable)
```

`make android-clean` drops the Docker volumes used as the Go module +
Gradle home cache. Only needed if a build wedges or after a major
toolchain bump.

## First-friend install (Step 10 of the plan)

For the operator's first end-to-end test on a real phone:

### One-time host setup

```sh
brew install --cask android-platform-tools   # gives you adb
```

Plug the phone in via USB. On the phone:
1. Settings → About → tap "Build number" 7 times → enables Developer Options.
2. Settings → System → Developer Options → enable **USB debugging**.
3. Plug in; accept the "Allow USB debugging?" dialog.

```sh
adb devices    # confirm the phone is listed
```

### Generate a per-friend config on the VPS

```sh
ssh beacongate-vps "cd /etc/beacongate && \
  beacongate-admin add-client \
    --server-config server_config.json \
    --name yourname \
    --output yourname.bg && \
  systemctl restart beacongate-server.service"
ssh beacongate-vps "cat /etc/beacongate/yourname.bg" > /tmp/yourname.bg
```

Note: `add-client` requires a `client_template` block in the server
config. If absent, the CLI's error message tells you exactly what to
add to `/etc/beacongate/server_config.json`.

### Build, install, and import

```sh
make android-apk-debug
adb install -r mobile/android/app/build/outputs/apk/debug/app-arm64-v8a-debug.apk
```

Open BeaconGate on the phone. Two paths to import:
- **Drive link**: upload `yourname.bg` to Drive (set "Anyone with the
  link can view"), copy the share URL, paste in the app, tap
  "Fetch from Drive".
- **File picker**: copy `yourname.bg` to the phone's Downloads via
  USB or Drive, tap "Pick from files", select it.

### Verify the connection

1. Tap **Connect**. Accept the system VPN dialog.
2. Wait for status to flip from "Connecting…" to "Connected".
3. On the phone, open Chrome and visit `https://api.ipify.org/`.
   The IP shown should be **91.98.140.78** (the VPS).
4. Visit a domain that's filtered in Iran (e.g. `github.com`). It
   should load — if it does, the tunnel is working end-to-end.

### Inspecting on the server

While the phone is connected:
```sh
ssh beacongate-vps "journalctl -u beacongate-server -f | grep yourname"
```
Should show `session.open` lines for your client_id, no
`tunnel.auth_failed` entries.

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
