# uTLS Fingerprint — Pin & Bump Cadence

BeaconGate's `appsscript` transport uses
[uTLS](https://github.com/refraction-networking/utls) to mimic a
real Chrome browser's TLS ClientHello on the wire, reducing naive
JA3/JA4 distinguishability. Without uTLS, the handshake
fingerprints as "Go" via JA3/JA4 — a passive DPI box can flag and
block traffic on that signal alone, even though the wire path ends
at a real Google IP with `SNI=www.google.com`.

**Important caveats — uTLS is not a complete defense.** It addresses
the static ClientHello fingerprint only. It does NOT defend against
active probing (a censor connecting to `script.google.com/macros/...`
paths to verify response shape), traffic-pattern analysis (long-poll
cadence, payload-size distribution, per-batch timing), Google-side
classifiers running on Apps Script itself, or future fingerprinting
methods that look at properties beyond the ClientHello. Treat uTLS
as one layer among several, not as a unblockability guarantee.

This document is the contract for keeping the ClientHello fingerprint
current.

## Current pin

| BeaconGate version | uTLS library version | Chrome profile | Notes |
|---|---|---|---|
| **v1.1.0** | `v1.8.2` | `HelloChrome_131` | Chrome 131 was stable Nov–Dec 2025; widely distributed in the real world today (auto-update lag means more users on 131 than on 133, so we blend with the larger population). |

The pin lives at [`engine/transport/appsscript/utls_dial.go`](../engine/transport/appsscript/utls_dial.go) (`pinnedProfile`). The library version lives in [`go.mod`](../go.mod).

## Why pinned, not `_Auto`

uTLS provides `utls.HelloChrome_Auto` — an alias that automatically
points at the latest Chrome profile uTLS supports. That sounds
convenient but means **our fingerprint silently changes every time we
bump uTLS**. A network observer could detect "BeaconGate just
upgraded" by watching for the fingerprint shift. Worse, an
inadvertent uTLS bump in CI could move us to a profile less
represented in the wild Chrome population, weakening the disguise
without anyone noticing.

Pinning to a specific Chrome version (e.g. `HelloChrome_131`) gives
us deterministic behavior and explicit, operator-visible
change-control. Bumps happen on our schedule, not uTLS's.

## Bump policy

**One Chrome major per BeaconGate minor release.** When v1.2.0 ships,
the pin moves to whatever the latest broadly-deployed Chrome stable
is at that time (likely `HelloChrome_133` or `HelloChrome_135` by
that point).

The criterion for the new pin is:

1. The Chrome version has been stable for **at least 4 weeks**, so
   real-world auto-update has propagated to a meaningful share of
   the wild population.
2. The Chrome version is **not the absolute latest** — the absolute
   latest fingerprint is over-represented in security-tooling
   captures and under-represented in real users (auto-update lag).
   Pin to "stable -1" or "stable -2" for better blending.
3. uTLS has a `HelloChrome_NN` constant for it. Using `_Auto` is
   forbidden (see above).

## How to bump

In a v1.2.0 / v1.3.0 / etc. release branch:

1. **Pick the new Chrome version.** Example: Chrome 137 has been
   stable since 2026-XX-XX, so pin to `HelloChrome_137` (assuming
   uTLS provides it; otherwise, the closest available).

2. **Confirm uTLS provides the constant.** Grep the upstream:

   ```sh
   curl -fsS https://raw.githubusercontent.com/refraction-networking/utls/master/u_common.go | \
     grep -E '^\s+HelloChrome_[0-9]'
   ```

3. **Bump the uTLS library** if needed:

   ```sh
   go get github.com/refraction-networking/utls@latest
   go mod tidy
   ```

4. **Edit the pin** in `engine/transport/appsscript/utls_dial.go`:

   ```go
   var pinnedProfile = utls.HelloChrome_137  // was HelloChrome_131
   ```

5. **Update this table** with the new row.

6. **Run the fingerprint test:**

   ```sh
   go test ./engine/transport/appsscript/ -run TestUTLSFingerprintIsChromeNotGo -count=1
   ```

   The test asserts structural invariants (ClientHello length,
   distinctive Chrome extensions). If the new profile passes, the
   bump is good. If it fails, the new profile may be stale or
   unsupported in the version of uTLS we have — investigate before
   shipping.

7. **Update `CHANGELOG.md`** under the new BeaconGate version's
   `### Changed` section: "uTLS fingerprint pin: HelloChrome_131 →
   HelloChrome_137 (Chrome 137 stable since YYYY-MM-DD)."

## CI early-warning

A future CI check will warn when our pinned profile is more than two
Chrome majors behind uTLS's `HelloChrome_Auto`. The warning fires in
CI as a non-blocking notice; the bump itself remains a deliberate
human decision.

For now, the cadence is enforced by code review on each minor
release: the release checklist (see `CONTRIBUTING.md`) includes
"Confirm `pinnedProfile` is current per `docs/uTLS-fingerprint-cadence.md`."

## What gets cheated and what doesn't

What uTLS closes (genuine win):

- **JA3/JA4 hash matches Chrome.** A passive observer running JA3
  fingerprinting sees what looks like a real Chrome browser.
- **Cipher suite list matches Chrome's.** Including Chrome's
  GREASE values, which Go's stdlib doesn't emit.
- **Extension list and order matches Chrome's** (modulo Chrome's
  extension shuffler, which uTLS reproduces).
- **TLS version negotiation looks like Chrome.** TLS 1.3 minimum
  with `supported_versions` extension carrying both 0x0303 and
  0x0304, like real Chrome.

What uTLS does *not* close:

- **Behavioral fingerprinting** — timing, payload-size patterns,
  request cadence. A sophisticated observer can still infer
  *that* you're tunneling, even if not *what*.
- **Active probing** — if a censor probes our endpoint and gets a
  response, the response shape might not match real Apps Script
  (this is the Apps Script forwarder's problem, not uTLS's, but
  it's a related residual risk).
- **TLS-version drift** — if Chrome moves to TLS 1.4 (hypothetical)
  before we bump, we'll stand out as a behind-the-times Chrome.
  The bump cadence is the answer.

## Related

- [`SECURITY.md`](../SECURITY.md) — full residual-risk model.
- [`engine/transport/appsscript/utls_dial.go`](../engine/transport/appsscript/utls_dial.go) — the pin and the dialer.
- [`engine/transport/appsscript/utls_test.go`](../engine/transport/appsscript/utls_test.go) — the fingerprint test.
- [github.com/refraction-networking/utls](https://github.com/refraction-networking/utls) — upstream library.
