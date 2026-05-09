# Security Policy

BeaconGate handles user network traffic and is therefore a security-sensitive
project. We take vulnerability reports seriously and prefer **private**
disclosure so we can ship a fix before the issue becomes exploitable in the
wild.

## Reporting a vulnerability

**Please do NOT open a public GitHub issue** for suspected security
vulnerabilities. Use **GitHub Private Vulnerability Reporting**: open
the *Security* tab of this repository and click *Report a vulnerability*.

GitHub will notify the maintainers privately and provide a coordinated-
disclosure workflow. We prefer this channel because it produces a clean
audit trail; if you are unable to use it, raise an issue requesting a
private channel and we will reply with one.

Please include:

- A description of the issue and its impact.
- A minimal proof-of-concept or reproduction steps.
- The version / commit you tested against.
- Whether you have already disclosed the issue to anyone else.

## Response timeline

We aim to:

- Acknowledge your report within **72 hours**.
- Provide an initial assessment within **7 days**.
- Ship a fix or mitigation within **30 days** for high-severity issues.

Embargo windows can be extended by mutual agreement when a fix is
operationally complex.

## Scope

In scope:

- Authentication or authorization bypass on the relay server, admin API,
  or local control API.
- Cryptographic flaws in the wire envelope (`engine/crypto`).
- SSRF, path traversal, or injection in the server runtime.
- Memory-corruption bugs (in Go these are rare but possible via cgo or
  unsafe).
- Denial-of-service vectors that allow a single client to disrupt other
  clients.

Out of scope (please do not file these as security issues):

- Vulnerabilities in dependencies — open a regular issue or PR upgrading
  the dependency.
- Issues that require physical access to a user's device.
- Self-service abuse such as a privileged operator misconfiguring policy.
- Theoretical timing side-channels with no practical exploit path.

## Hardening defaults you should be aware of

- The server's default policy refuses to dial private, loopback,
  link-local, multicast, or cloud-metadata addresses (SSRF guard).
- Both client transports (`https` and `appsscript`) enforce TLS 1.3
  minimum.
- The admin API rate-limits authentication failures (8 / 5 min / IP).
- Per-client session caps (default 100) and idle-session reaping
  (default 10 min) protect against single-tenant DoS.
- v1.1 wire envelope: per-client AEAD keys derived from the master
  via HKDF-SHA256, AAD-bound `client_id` (a captured packet posted
  with a different cleartext id fails authentication), inner
  timestamp + 16-byte replay-id, server-side replay-dedup cache with
  a ±5min skew window and a 60s response-cache for idempotent retry.

If you find a configuration where these defaults are weakened, please
report it before relying on them.

## What the `appsscript` transport is and is NOT

The `appsscript` transport routes traffic through a user-deployed
Google Apps Script web app so that, on the network, a passive observer
sees TLS to a Google IP with `SNI=www.google.com` and HTTP `Host:
script.google.com`. Blocking this path cleanly requires also blocking
`script.google.com`, which has collateral damage to legitimate Apps
Script users. **It raises the cost of blocking; it does not make
blocking impossible.**

Residual risks NOT eliminated by this transport:

- **Traffic-pattern analysis** — long-poll cadence, payload-size
  distribution, per-batch timing. A determined adversary with full
  packet capture can fingerprint these patterns even though the
  bytes are encrypted.
- **TLS-fingerprint analysis (JA3 / JA4)** — **Closed in v1.1.0** for
  the default build. The `appsscript` transport now uses
  [`github.com/refraction-networking/utls`](https://github.com/refraction-networking/utls)
  to emit a Chrome 131 ClientHello byte-identical to a real browser.
  See [`docs/uTLS-fingerprint-cadence.md`](docs/uTLS-fingerprint-cadence.md)
  for the pin and bump policy. **Residual sub-risks:**
  (a) the pinned Chrome version drifts behind real-world Chrome
  between BeaconGate minor releases; the bump cadence is the
  mitigation. (b) New fingerprinting techniques beyond JA3/JA4
  could emerge; uTLS's coverage is best-known-state, not future-proof.
  (c) Compile-time tampering or a forced downgrade to stdlib `tls`
  via build tags re-opens the gap — operators relying on the disguise
  should verify the JA3 hash on their built binary.
- **Google-side classifiers** — Google can see the encrypted bytes
  pass through Apps Script and could in principle deploy
  classifiers; the transport only protects against off-Google
  observers.
- **URL-pattern blocking** — a censor that targets
  `script.google.com/macros/s/.../exec` specifically (rather than
  `script.google.com` wholesale) breaks this path with much less
  collateral damage.
- **Per-deployment ToS enforcement** — Google can suspend specific
  deployments at any time; multi-deployment failover is partial
  mitigation, not a guarantee.

Do not paraphrase any of this as "undetectable" or "unblockable" in
documentation, marketing copy, or user-facing UI. The transport's
honest claim is "looks like Google traffic to network-layer DPI",
nothing more.
