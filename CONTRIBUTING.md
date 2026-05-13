# Contributing to BeaconGate

Thanks for considering a contribution. BeaconGate is a multi-language
monorepo; the rules below cover the Go subtree, plus a few that apply to
every language.

## Before you start

- For non-trivial changes, open an issue first so we can agree on the
  shape of the work before code is written.
- Security fixes follow the private channel described in
  [SECURITY.md](SECURITY.md), not a public PR.

## Layout

The repo is organized by **product domain** at the top level. Each
domain picks the language best suited to it:

```
cmd/                     Go binaries
engine/, client/, server/, test/   Go (the relay engine)
mobile/android/          Android native app (Kotlin, gomobile-bound Go core)
mobile/ios/              reserved for iOS (language TBD)
desktop/                 reserved for desktop (language TBD)
protocol/                cross-language protocol spec / schemas
ops/                     deployment assets (cross-cutting)
docs/                    product docs (cross-cutting)
tools/                   dev/build helpers (cross-cutting)
```

When adding a new language subtree:

1. Put the language toolchain config (`package.json`, `Cargo.toml`,
   `*.xcodeproj`, `build.gradle.kts`, …) at the **subtree root**, not at
   the repo root.
2. The subtree must be self-contained: its build does not reach into
   another language's directory. Cross-language coupling lives in
   `protocol/` (schemas) or via the loopback control API.
3. Add a top-level Makefile target that delegates to the subtree's own
   build (for example `make desktop-build`).

## Local development (Go subtree)

Requirements: Go 1.25 or newer.

```sh
make build        # ./bin/{beacongate-client,beacongate-server,beacongate-admin}
make test         # go test ./...
make race         # go test -race ./...
make vet          # go vet ./...
make fmt          # gofmt -w .
make lint         # golangci-lint run (optional but encouraged)
make ci           # everything CI runs
```

## Coding standards (Go)

- Format with `gofmt` and pass `go vet` and `golangci-lint`.
- Tests live next to the source they cover; cross-package tests go in
  `test/integration/`.
- Add a comment ONLY when *why* is non-obvious. The code should speak
  for *what* it does.
- Do not introduce circular package dependencies.
- Honour the SSRF guard, TLS 1.3 floor, and rate-limit defaults — every
  weakening must be a conscious, documented opt-in.

Style for non-Go subtrees will be defined when the first one lands. Each
subtree's `CONTRIBUTING.md` (if any) supplements this file.

## Commit messages

We follow the conventional shape:

```
type(scope): short summary in imperative mood

Optional body explaining the why.
```

`type` is one of `feat`, `fix`, `docs`, `refactor`, `test`, `chore`,
`ops`, `security`. `scope` is the directory the change touches, e.g.
`engine/protocol`, `server/runtime`, `android`.

## Submitting a pull request

1. Branch from `master`.
2. Run `make ci` locally; CI runs the same checks and will block on
   failures.
3. PR description should include:
   - what changed and why,
   - test plan,
   - any operator-visible behavior change.
4. Keep PRs focused. A 200-line PR is faster to review than a 2000-line
   one even if it ships less.

## License

By contributing, you agree your contribution is licensed under the
Apache License 2.0 (see [LICENSE](LICENSE)).
