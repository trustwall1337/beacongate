# BeaconGate Android

Native Android client for BeaconGate. Captures all phone traffic via the
system VPN, routes it through the BeaconGate tunnel, and exits at the
operator's VPS.

## Architecture

The Go client (`client/runtime` + `engine/*`) is exposed to Kotlin via
`gomobile bind`. The Android app wraps it with:

1. **`BeaconVpnService`** — Android system VPN service that captures
   phone traffic into a TUN device.
2. **tun2socks** (Go library, bundled in the same `.aar`) — translates
   raw IP packets from TUN into SOCKS5 connections to the local
   listener.
3. **gomobile facade** (`mobile/bindings/`) — exposes `ImportConfig`,
   `StartTunnel`, `StopTunnel`, `Status`, `GetStats`, `Version`,
   `SetLogSink` to Kotlin.
4. **Kotlin UI** — single-screen `MainActivity` with config import
   (Drive link or file picker), connect/disconnect, and status
   indicator.

## Building

The build is **fully Dockerized** — only Docker is required on the
host. Phone testing uses the host's `adb`.

```sh
# One-time: build the Docker image with SDK + NDK + Gradle (~10 min).
make android-build-image

# Iteration:
make android-aar         # bin/beacongate.aar (gomobile bind)
make android-apk         # release APK (~15 MB)
make android-apk-debug   # debug-signed APK (~20 MB, installable via adb)
```

`make android-clean` drops the Docker volumes used as the Go module
and Gradle home caches. Only needed if a build wedges or after a major
toolchain bump.

## End-to-end install

### 1. Prepare the host

```sh
brew install --cask android-platform-tools   # provides adb
```

On the phone:
1. Settings → About → tap **Build number** 7 times → enables Developer
   Options.
2. Settings → System → Developer Options → enable **USB debugging**.
3. Plug in via USB; accept the *Allow USB debugging?* dialog.

```sh
adb devices    # confirm the phone is listed
```

### 2. Generate a per-client config on the VPS

```sh
ssh beacongate-vps "cd /etc/beacongate && \
  beacongate-admin add-client \
    --server-config server_config.json \
    --name alice \
    --output alice.json && \
  systemctl restart beacongate-server.service"
ssh beacongate-vps "cat /etc/beacongate/alice.json" > /tmp/alice.json
```

`add-client` requires a `client_template` block in the server config;
the CLI's error message lists exactly what to add to
`/etc/beacongate/server_config.json` if it's absent.

### 3. Install and import

```sh
make android-apk
adb install -r mobile/android/app/build/outputs/apk/release/app-arm64-v8a-release.apk
```

Open BeaconGate on the phone. Two import paths:

- **Drive link** — upload `alice.json` to Drive (*Anyone with the link
  can view*), copy the share URL, paste in the app, tap **Fetch from
  Drive**.
- **File picker** — copy `alice.json` to the phone's `Downloads/` via
  USB or Drive, tap **Pick from files**, select it.

### 4. Verify

1. Tap **Connect**. Accept the system VPN dialog.
2. Wait for status to flip from *Connecting…* to *Connected*.
3. On the phone, open Chrome and visit `https://api.ipify.org/`. The
   IP shown should match your VPS's public IP.
4. Visit a destination you expect to work. If it loads, the tunnel is
   working end-to-end.

### 5. Inspect on the server (optional)

```sh
ssh beacongate-vps "journalctl -u beacongate-server -f | grep alice"
```

Expect `session.open` lines for the client_id and no
`tunnel.auth_failed` entries.

## Local development with Android Studio

The Dockerized path is canonical; Android Studio is optional and useful
mainly for UI iteration and on-device debugging.

1. `brew install --cask android-studio`
2. Open `mobile/android/` as a project. Studio resolves the Gradle
   wrapper (8.7) and pulls dependencies on first sync.
3. Build the AAR via the Docker target (`make android-aar`) before
   running — the Gradle build expects `app/libs/beacongate.aar` to
   exist.

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
        │   ├── MainActivity.kt
        │   ├── MainViewModel.kt
        │   ├── ConnectionState.kt
        │   ├── ConfigImport.kt
        │   ├── CredentialStore.kt
        │   ├── BeaconVpnService.kt
        │   └── TrafficScopeStore.kt
        └── res/                # strings, themes, layout
```
