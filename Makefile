.PHONY: all ci clean help \
        go-build go-build-android go-test go-race go-vet go-fmt go-fmt-check go-lint go-tidy go-bench go-fuzz \
        build build-android test race vet fmt fmt-check lint tidy bench fuzz \
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
	@echo "  make mobile-build, mobile-test"
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

# --- Mobile subtree (placeholder) --------------------------------------

mobile-build:
	@echo "mobile/ has no implementation yet (Phase 4)"

mobile-test:
	@echo "mobile/ has no implementation yet (Phase 4)"

# --- Aggregate aliases --------------------------------------------------

build: go-build
build-android: go-build-android
test: go-test
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
