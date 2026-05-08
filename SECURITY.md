# Security Policy

BeaconGate handles user network traffic and is therefore a security-sensitive
project. We take vulnerability reports seriously and prefer **private**
disclosure so we can ship a fix before the issue becomes exploitable in the
wild.

## Reporting a vulnerability

**Please do NOT open a public GitHub issue** for suspected security
vulnerabilities. Use one of the following private channels instead:

1. **GitHub Private Vulnerability Reporting** (preferred): open the
   *Security* tab of this repository and click *Report a vulnerability*.
2. **Email**: send a description to `security@<your-domain>` (replace with
   the actual address before publishing this file).

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
- The Google-fronted client transport enforces TLS 1.3 minimum.
- The admin API rate-limits authentication failures (8 / 5 min / IP).
- Per-client session caps (default 100) and idle-session reaping
  (default 10 min) protect against single-tenant DoS.
- The wire envelope is AEAD-encrypted with a 32-byte AES-256-GCM key.

If you find a configuration where these defaults are weakened, please
report it before relying on them.
