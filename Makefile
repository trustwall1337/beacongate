.PHONY: all ci clean help \
        go-build go-test go-race go-vet go-fmt go-fmt-check go-lint go-tidy \
        build test race vet fmt fmt-check lint tidy \
        desktop-build desktop-test mobile-build mobile-test

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
	@echo "  make build       - build everything that exists today (currently: Go)"
	@echo "  make test        - run all tests (currently: Go)"
	@echo "  make race        - tests with -race detector"
	@echo "  make ci          - everything CI runs"
	@echo ""
	@echo "Language-specific (Go):"
	@echo "  make go-build    - build Go binaries to ./bin/"
	@echo "  make go-test     - go test ./..."
	@echo "  make go-race     - go test -race ./..."
	@echo "  make go-vet      - go vet ./..."
	@echo "  make go-fmt      - gofmt -w ."
	@echo "  make go-lint     - golangci-lint run"
	@echo "  make go-tidy     - go mod tidy"
	@echo ""
	@echo "Future subtree targets (no-ops until the subtree exists):"
	@echo "  make desktop-build, desktop-test"
	@echo "  make mobile-build, mobile-test"

# --- Go subtree ---------------------------------------------------------

go-build:
	$(GO) build -o $(BIN)/ ./cmd/...

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
test: go-test
race: go-race
vet: go-vet
fmt: go-fmt
fmt-check: go-fmt-check
lint: go-lint
tidy: go-tidy

clean:
	rm -rf $(BIN) dist coverage.out

# ci runs every check the CI job runs locally.
ci: go-fmt-check go-vet go-build go-test go-race
	@echo "ci passed"
