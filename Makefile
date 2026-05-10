.PHONY: all ci clean help \
        go-build go-build-android go-test go-race go-vet go-fmt go-fmt-check go-lint go-tidy go-bench go-fuzz \
        build build-android test race vet fmt fmt-check lint tidy bench fuzz release \
        desktop-build desktop-test mobile-build mobile-test \
        docker-build docker-init docker-up docker-down docker-logs docker-status docker-clean

# Top-level orchestrator for the BeaconGate monorepo. Each language subtree
# has its own targets; the root targets without a prefix delegate to the Go
# subtree today and will fan out to siblings (desktop/, mobile/) when those
# subtrees are added. Add explicit go-* aliases so callers can be specific.

GO ?= go
BIN ?= bin
GOPKGS = ./...

all: build

help:
	@echo "Top-level targets:"
	@echo "  make build         - build everything that exists today (currently: Go)"
	@echo "  make build-android - cross-compile the client for Android (linux/arm64, runs in Termux)"
	@echo "  make release       - build all 6 release archives locally (dry-run of release pipeline)"
	@echo "  make test          - run all tests (currently: Go)"
	@echo "  make race          - tests with -race detector"
	@echo "  make bench         - run benchmarks (engine/transport/appsscript)"
	@echo "  make fuzz          - run fuzz tests for 30s each (envelope decode + crypto Open)"
	@echo "  make ci            - everything CI runs"
	@echo ""
	@echo "Language-specific (Go):"
	@echo "  make go-build         - build Go binaries to ./bin/"
	@echo "  make go-build-android - cross-compile beacongate-client for linux/arm64 (Termux on Android)"
	@echo "  make go-test          - go test ./..."
	@echo "  make go-race          - go test -race ./..."
	@echo "  make go-vet           - go vet ./..."
	@echo "  make go-fmt           - gofmt -w ."
	@echo "  make go-lint          - golangci-lint run"
	@echo "  make go-tidy          - go mod tidy"
	@echo ""
	@echo "Future subtree targets (no-ops until the subtree exists):"
	@echo "  make desktop-build, desktop-test"
	@echo ""
	@echo "Mobile (Android, native app — Docker-driven build, phone via host adb):"
	@echo "  make mobile-build         - alias for android-aar"
	@echo "  make mobile-test          - go test ./mobile/..."
	@echo "  make android-build-image  - build the Android build Docker image (~10 min first run)"
	@echo "  make android-aar          - produce bin/beacongate.aar via gomobile bind (Docker)"
	@echo "  make android-apk          - produce a release APK via Gradle (Docker; unsigned, ~15 MB)"
	@echo "  make android-apk-debug    - produce a debug-signed APK installable via 'adb install' (Docker; ~20 MB)"
	@echo "  make android-clean        - drop Docker build caches (Go module + Gradle)"
	@echo ""
	@echo "Docker (server only):"
	@echo "  make docker-up      - bootstrap config + build + start (one-shot)"
	@echo "  make docker-down    - stop, keep config + policy store"
	@echo "  make docker-logs    - tail server logs"
	@echo "  make docker-status  - compose ps + healthz reachability"
	@echo "  make docker-clean   - stop + delete the policy store volume"
	@echo "  make docker-build   - build the image only"
	@echo "  make docker-init    - generate config (idempotent)"

# --- Go subtree ---------------------------------------------------------

go-build:
	$(GO) build -o $(BIN)/ ./cmd/...

# go-build-android cross-compiles beacongate-client for linux/arm64.
# Termux is a Linux userland on Android, so a static linux/arm64 binary
# runs there without an NDK or Android SDK. CGO is off so no glibc
# dependency leaks in. -trimpath strips the operator's local paths;
# -ldflags="-s -w" drops the symbol/DWARF tables for a smaller binary.
go-build-android:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
	  $(GO) build -trimpath -ldflags="-s -w" \
	  -o $(BIN)/beacongate-client-android-arm64 ./cmd/beacongate-client

# android-build-image builds the Docker image that contains the
# Android SDK + NDK + JDK 17 + gomobile + the matching Go toolchain.
# First build is ~10–15 minutes (downloads ~3 GB); subsequent builds
# are cached and complete in seconds.
#
# Operators only need Docker installed on their machine — no local
# Android SDK / NDK / JDK.
android-build-image:
	docker build \
	  --platform linux/amd64 \
	  -f ops/docker/Dockerfile.android \
	  -t beacongate-android-build:latest \
	  ops/docker

# android-aar produces the Android Archive (.aar) that the native
# Android app links against. Wraps mobile/bindings (the gomobile-clean
# facade over the existing Go client) into a Java/Kotlin-callable
# library. arm64-only — covers every Android phone made since ~2017
# and is the only ABI we ship in the production APK (per the v1
# size-budget plan; armeabi-v7a + x86_64 add ~9 MB combined).
#
# Runs gomobile bind inside the beacongate-android-build container
# so no local Android SDK / NDK / JDK is required. The host's
# `bin/` directory is bind-mounted as the output sink.
#
# Caches:
#   - bg-android-go-mod : Go module download cache (survives
#     image rebuilds, not build-context changes)
#   - bg-android-go-build : gomobile's intermediate Go object cache
#
# -ldflags="-s -w" + -trimpath strip debug symbols and absolute paths
# from the embedded Go runtime, which materially reduces the .aar
# size (target: < 13 MB; final APK target: < 20 MB).
android-aar: android-build-image
	mkdir -p $(BIN)
	docker run --rm \
	  --platform linux/amd64 \
	  -v "$(CURDIR):/src" \
	  -v bg-android-go-mod:/root/go/pkg/mod \
	  -v bg-android-go-build:/root/.cache/go-build \
	  -w /src \
	  beacongate-android-build:latest \
	  gomobile bind \
	    -target=android/arm64 \
	    -androidapi=24 \
	    -ldflags="-s -w" \
	    -trimpath \
	    -o bin/beacongate.aar \
	    ./mobile/bindings
	@echo
	@echo "Built $(BIN)/beacongate.aar"
	@du -h $(BIN)/beacongate.aar
	@# Place a copy under the Gradle module's libs/ directory so
	@# `make android-apk` (and Android Studio) can resolve the
	@# `fileTree("libs")` dependency without an extra step.
	@if [ -d mobile/android/app ]; then \
	  mkdir -p mobile/android/app/libs && \
	  cp $(BIN)/beacongate.aar mobile/android/app/libs/beacongate.aar && \
	  echo "Mirrored to mobile/android/app/libs/beacongate.aar"; \
	fi

# android-apk-debug builds an installable debug-signed APK. Use this
# for on-device testing — the release APK (`android-apk`) is
# unsigned and cannot be installed without a release keystore.
# Output is ~20 MB (debug APKs include symbols + skip R8); the
# release path produces ~15 MB.
android-apk-debug: android-aar
	@if [ ! -f mobile/android/settings.gradle.kts ]; then \
		echo "mobile/android/ has no Gradle project yet (Step 3 not done)."; \
		exit 0; \
	fi
	docker run --rm \
	  --platform linux/amd64 \
	  -v "$(CURDIR):/src" \
	  -v bg-android-gradle:/root/.gradle \
	  -w /src/mobile/android \
	  beacongate-android-build:latest \
	  gradle :app:assembleDebug --no-daemon
	@echo
	@echo "Debug APK (installable via 'adb install'):"
	@find mobile/android/app/build/outputs/apk/debug -name '*.apk' -exec du -h {} \;

# android-apk builds the release APK via Gradle inside the Docker
# image. Runs after `android-aar` produces the AAR the Gradle
# project links. No-op until Step 3 of the Android plan lands the
# Gradle skeleton under mobile/android/.
android-apk: android-aar
	@if [ ! -f mobile/android/settings.gradle.kts ]; then \
		echo "mobile/android/ has no Gradle project yet (Step 3 not done)."; \
		exit 0; \
	fi
	docker run --rm \
	  --platform linux/amd64 \
	  -v "$(CURDIR):/src" \
	  -v bg-android-gradle:/root/.gradle \
	  -w /src/mobile/android \
	  beacongate-android-build:latest \
	  gradle :app:assembleRelease --no-daemon
	@echo
	@echo "Find the APK under mobile/android/app/build/outputs/apk/release/"
	@find mobile/android/app/build/outputs/apk/release -name '*.apk' -exec du -h {} \;

# android-clean removes Docker volumes used for caching the Android
# build. Use when the build state seems wedged or after a major
# Gradle / SDK upgrade.
android-clean:
	-docker volume rm bg-android-go-mod bg-android-go-build bg-android-gradle

go-test:
	@if ! find . -name '*.go' -not -path './vendor/*' -print -quit | grep -q .; then \
		echo "no go packages to test"; \
	else \
		$(GO) test $(GOPKGS) -count=1; \
	fi

go-race:
	$(GO) test -race $(GOPKGS) -count=1

go-vet:
	$(GO) vet $(GOPKGS)

go-fmt:
	gofmt -w .

go-fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt found unformatted files:"; echo "$$out"; exit 1; fi

go-lint:
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not installed; see https://golangci-lint.run/"; exit 1; }
	golangci-lint run

go-tidy:
	$(GO) mod tidy

# go-bench runs the in-process performance benchmarks. Plan A7 #8–13:
# numbers are an internal floor only; real Apps Script p50/p95
# measurements need a deployment with a real network leg (see
# apps_script/README.md and docs/deployment.md "B7. Verify").
go-bench:
	$(GO) test -bench=. -benchmem -benchtime=1s -timeout 120s -run='^$$' ./engine/transport/appsscript/...

# go-fuzz runs both fuzz targets for 30s each. Use longer fuzztime
# in CI when the budget allows.
go-fuzz:
	$(GO) test -fuzz=FuzzOpen -fuzztime=30s ./engine/crypto/
	$(GO) test -fuzz=FuzzDecodeEnvelope -fuzztime=30s ./engine/protocol/

# --- Desktop subtree (placeholder) -------------------------------------

desktop-build:
	@if [ -f desktop/package.json ] || [ -f desktop/Cargo.toml ]; then \
		$(MAKE) -C desktop build; \
	else \
		echo "desktop/ has no implementation yet (Phase 3)"; \
	fi

desktop-test:
	@if [ -f desktop/package.json ] || [ -f desktop/Cargo.toml ]; then \
		$(MAKE) -C desktop test; \
	else \
		echo "desktop/ has no implementation yet (Phase 3)"; \
	fi

# --- Mobile subtree ----------------------------------------------------

# mobile-build delegates to android-aar (the gomobile artifact). When
# the Gradle Android project lands under mobile/android/, this target
# will additionally invoke `./gradlew :app:assembleRelease`.
mobile-build: android-aar

# mobile-test runs the Go-side mobile/bindings unit tests. The
# Kotlin-side tests live under mobile/android/app/src/test/ and are
# driven by Gradle (`./gradlew test`); CI invokes both in sequence.
mobile-test:
	$(GO) test ./mobile/...

# --- Aggregate aliases --------------------------------------------------

build: go-build
build-android: go-build-android
test: go-test

# release builds all six release archives locally (mirrors what
# .github/workflows/release.yml does in CI). Output: dist/release/.
# Useful for testing the release pipeline without pushing a tag.
# Does NOT run cosign signing — that needs GitHub OIDC and only
# runs in CI.
release:
	@rm -rf dist/release dist/staging
	@mkdir -p dist/release
	@TAG="$$(git describe --tags --always 2>/dev/null || echo dev)"; \
	for target in linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64 android-arm64; do \
	  case "$$target" in \
	    linux-*)   GOOS=linux;   GOARCH=$${target#linux-} ;; \
	    darwin-*)  GOOS=darwin;  GOARCH=$${target#darwin-} ;; \
	    windows-*) GOOS=windows; GOARCH=$${target#windows-} ;; \
	    android-*) GOOS=linux;   GOARCH=$${target#android-} ;; \
	  esac; \
	  ext=""; if [ "$$GOOS" = "windows" ]; then ext=".exe"; fi; \
	  rm -rf dist/staging && mkdir -p dist/staging; \
	  for cmd in beacongate-client beacongate-server beacongate-admin; do \
	    GOOS=$$GOOS GOARCH=$$GOARCH CGO_ENABLED=0 \
	      $(GO) build -trimpath -ldflags="-s -w" \
	      -o "dist/staging/$$cmd$$ext" "./cmd/$$cmd" || exit 1; \
	  done; \
	  cp client_config.example.json client_config.appsscript.example.json server_config.example.json README.md LICENSE CHANGELOG.md dist/staging/; \
	  base="BeaconGate-$$TAG-$$target"; \
	  if [ "$$GOOS" = "windows" ]; then \
	    ( cd dist/staging && zip -q -X -r "../release/$$base.zip" . ); \
	  else \
	    ( cd dist/staging && tar -czf "../release/$$base.tar.gz" . ); \
	  fi; \
	  echo "  built dist/release/$$base"; \
	done; \
	( cd dist/release && sha256sum *.tar.gz *.zip > "BeaconGate-$$TAG-checksums.txt" ); \
	echo "release archives in dist/release/"


race: go-race
vet: go-vet
fmt: go-fmt
fmt-check: go-fmt-check
lint: go-lint
tidy: go-tidy
bench: go-bench
fuzz: go-fuzz

clean:
	rm -rf $(BIN) dist coverage.out

# ci runs every check the CI job runs locally.
ci: go-fmt-check go-vet go-build go-test go-race
	@echo "ci passed"

# --- Docker (server only) ----------------------------------------------
#
# All docker-* targets operate on ops/docker/docker-compose.yml. The image
# is built from this repo's Go source. The client and admin CLI run on
# the operator's laptop, NOT in the container.

DOCKER_COMPOSE ?= docker compose
COMPOSE_FILE = ops/docker/docker-compose.yml
DOCKER_CONFIG_DIR = ops/docker/config
DOCKER_CONFIG_FILE = $(DOCKER_CONFIG_DIR)/server_config.json
DOCKER_CONFIG_TEMPLATE = ops/docker/server_config.template.json

docker-build:
	$(DOCKER_COMPOSE) -f $(COMPOSE_FILE) build

# docker-init generates ops/docker/config/server_config.json with a fresh
# AEAD key. Idempotent: re-running with an existing config is a no-op so
# you cannot accidentally rotate the key by re-running docker-up.
docker-init:
	@if [ -f "$(DOCKER_CONFIG_FILE)" ]; then \
		echo "$(DOCKER_CONFIG_FILE) exists; not overwriting (delete it to rotate the key)"; \
		exit 0; \
	fi
	@if [ ! -f "$(DOCKER_CONFIG_TEMPLATE)" ]; then \
		echo "missing template: $(DOCKER_CONFIG_TEMPLATE)"; \
		exit 1; \
	fi
	@mkdir -p $(DOCKER_CONFIG_DIR)
	@$(GO) build -o $(BIN)/beacongate-admin ./cmd/beacongate-admin
	@KEY=$$($(BIN)/beacongate-admin gen-key) && \
		sed "s|KEY_PLACEHOLDER|$$KEY|" $(DOCKER_CONFIG_TEMPLATE) > $(DOCKER_CONFIG_FILE) && \
		chmod 600 $(DOCKER_CONFIG_FILE)
	@echo "wrote $(DOCKER_CONFIG_FILE) (mode 600)"
	@echo "the AEAD key is in that file — back it up before sharing the host"

docker-up: docker-init
	$(DOCKER_COMPOSE) -f $(COMPOSE_FILE) up -d --build
	@echo "waiting for healthcheck..."
	@for i in 1 2 3 4 5 6 7 8 9 10; do \
		status=$$(docker inspect --format='{{.State.Health.Status}}' beacongate-server 2>/dev/null || echo unknown); \
		case "$$status" in \
			healthy) echo "server healthy"; exit 0 ;; \
			unhealthy) echo "server reported unhealthy:"; \
				$(DOCKER_COMPOSE) -f $(COMPOSE_FILE) logs --tail=30 beacongate-server; exit 1 ;; \
			*) sleep 3 ;; \
		esac; \
	done; \
	echo "still not healthy after 30s; recent logs:"; \
	$(DOCKER_COMPOSE) -f $(COMPOSE_FILE) logs --tail=30 beacongate-server; \
	exit 1

docker-down:
	$(DOCKER_COMPOSE) -f $(COMPOSE_FILE) down

docker-logs:
	$(DOCKER_COMPOSE) -f $(COMPOSE_FILE) logs -f beacongate-server

docker-status:
	@$(DOCKER_COMPOSE) -f $(COMPOSE_FILE) ps
	@echo ""
	@printf "host  -> container healthz: "
	@if curl -fs --max-time 3 http://127.0.0.1:8080/healthz >/dev/null 2>&1; then echo "ok"; else echo "FAIL"; fi

docker-clean:
	$(DOCKER_COMPOSE) -f $(COMPOSE_FILE) down -v
	@echo "container and policy-store volume removed; live config kept (delete $(DOCKER_CONFIG_FILE) to rotate the key)"
