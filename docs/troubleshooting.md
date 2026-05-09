# BeaconGate Troubleshooting Runbook

This is the operator's first-response guide when something goes wrong
in production. It covers both transports (`https` and `appsscript`)
and both server- and client-side failures.

If the problem is on the friend's phone, share
[android-termux.md](android-termux.md) — it covers Termux-specific
issues (battery doze, NekoBox DNS leak) that aren't repeated here.

For deployment from scratch, see [deployment.md](deployment.md).
For policy-rule operations, see [policy.md](policy.md).

---

## Quick triage

Run these three checks in order. Most problems are caught by one of them.

```sh
# 1. Server alive?
curl -fsS https://relay.your-domain.com/healthz

# 2. Client thinks tunnel is up?
curl -fsS http://127.0.0.1:9091/api/status | jq
# look at .state — should be "connected"

# 3. SOCKS5 actually routes?
curl -x socks5h://127.0.0.1:1080 -fsS https://api.ipify.org
# should return the server's public IP, not yours
```

If all three pass, the tunnel is healthy. If any fails, jump to the
matching section below.

---

## Symptom → first response

### `cosign verify-blob` fails on a downloaded release

The signature on the release artifact didn't validate against the
BeaconGate workflow's GitHub OIDC identity. **Stop — do not extract
or run the binary.**

Possible causes (in decreasing likelihood):

- **Wrong files paired.** You're verifying `vX.Y.Z-checksums.txt` with
  a `.sig` and `.cert` from a *different* release. Re-download all
  three from the same Release page and retry.
- **Network corruption.** A partial or corrupted download. Re-run
  the download.
- **Repo identity changed.** The plan committed to keeping the
  release workflow at `https://github.com/trustwall1337/beacongate/.github/workflows/release.yml`.
  If the repo has moved/renamed, the `--certificate-identity-regexp`
  in your verify command is no longer correct. Check the latest
  Release notes for the current verify command before lowering your
  guard.
- **Genuine tampering.** Rare but possible (compromised GitHub
  account, malicious mirror, etc.). Open an issue on the repo
  flagging the fingerprint mismatch; do not run the artifact.

The [`scripts/install.sh`](../scripts/install.sh) one-liner runs the
same verification before unpacking. If it aborts at the verification
step, the same triage applies.

### Build fails with "JA3 mismatch" or `TestUTLSFingerprintIsChromeNotGo` test failure

The uTLS fingerprint test asserts the ClientHello structure matches
Chrome 131. Failures usually mean one of:

- **uTLS library bumped without bumping the pin.** Someone ran
  `go get -u github.com/refraction-networking/utls` and the new
  library doesn't include `HelloChrome_131` (very unlikely — uTLS
  is additive) or has changed its emission for an existing profile
  (also rare). Check
  [`docs/uTLS-fingerprint-cadence.md`](uTLS-fingerprint-cadence.md)
  for the current pin and the bump procedure.
- **Pinned profile bumped without updating expected invariants.**
  The test asserts certain extensions (`application_settings`,
  `compress_certificate`) are present. If a new Chrome version
  drops one of these, the test should be updated as part of the
  pin bump.
- **Stdlib `tls` accidentally re-introduced.** A merge conflict or
  refactor reverted `engine/transport/appsscript/fronting.go` to
  use stdlib `crypto/tls`. The test catches this — that's why it
  exists. Re-read [`engine/transport/appsscript/utls_dial.go`](../engine/transport/appsscript/utls_dial.go)
  and confirm `dialUTLS` is still the active path.

### `auth failed` (HTTP 401) on `/tunnel`

The server's AEAD key and the client's AEAD key don't match.

```sh
# server side
sudo grep -E '"key"' /etc/beacongate/server_config.json | cut -d'"' -f4
# client side
grep -E '"key"' client_config.json | cut -d'"' -f4
```

The two strings must be identical (base64). If they differ:

- Pick one as canonical, copy it into the other config, and restart
  both ends (`systemctl restart beacongate-server` server-side).
- If you're not sure which is correct: regenerate
  (`beacongate-admin gen-key`), put the new value in both configs,
  rebuild any operator bundle (`ops/prepare-bundle.sh`), and reissue
  to clients. **The old key is hard-cut** — older bundles will keep
  failing.

### `transport.healthy=false` at startup, `https` mode

The client cannot reach the server's public URL. This is a network
problem, not a BeaconGate problem.

```sh
# from the client host:
curl -v https://relay.your-domain.com/healthz
# common errors:
#   "could not resolve host"      → DNS misconfigured
#   "Connection refused"          → reverse proxy not listening
#   "SSL certificate problem"     → cert chain or expired cert
#   "no route to host"            → firewall (iptables / cloud SG)
```

Reverse-proxy specific:

- **Caddy:** `caddy validate --config /etc/caddy/Caddyfile`
- **Nginx:** `nginx -t` and check `proxy_pass` points at `localhost:8080`
- **Cloudflare in front:** confirm "Always Use HTTPS" is ON and the
  origin cert is valid; Cloudflare-flexible mode is not supported.

### `transport.healthy=false` at startup, `appsscript` mode

The client cannot reach Google or the Apps Script deployment is broken.

```sh
# 1. is Google reachable at all?
curl -fsS https://www.google.com/generate_204
# must return HTTP 204; if not, your network blocks Google

# 2. is THIS deployment ID alive?
curl -fsS https://script.google.com/macros/s/$DEPLOYMENT_ID/exec
# must return JSON like {"ok":true,"date":"...","count":N}
# if you get an HTML error page or a permission prompt → redeploy
```

Common appsscript failure modes:

- **Wrong DEPLOYMENT_ID in `script_keys`** — copy-paste error from
  the Apps Script console. Re-copy from the deployments page.
- **Old DEPLOYMENT_ID** — you edited `Code.gs` but didn't redeploy.
  Saving the file does NOT update what clients see; you must
  Deploy → New deployment.
- **Authorization not completed** — first-time deployment requires
  clicking through Google's "this script needs permissions" prompt
  while signed into the deploying Google account.
- **`RELAY_URL` in `Code.gs` wrong** — points at a non-existent or
  unreachable BeaconGate server. Edit line 26 and redeploy.
- **All deployments quota-blacklisted** — `script.google.com` returns
  HTTP 429; you've hit the ~20K UrlFetchApp invocations/day per
  Google account. Wait for midnight Pacific or add more
  `script_keys` from additional Google accounts.

### `probe.ok=false` but `transport.healthy=true`

The transport reaches the server, but the BeaconGate handshake fails.
Almost always a key, version, or client_id problem.

```sh
# server logs will show the precise reason:
journalctl -u beacongate-server -n 50 | grep -E 'tunnel\.(auth_failed|version_mismatch|replay)'
```

- `tunnel.auth_failed` — see "auth failed" above.
- `tunnel.version_mismatch` — running a v1.0 client against a v1.1
  server, or vice versa. The wire is hard-cut between major versions;
  align the binaries.
- `tunnel.replay_dropped` — packet replay defense rejected a stale
  envelope. Usually clock skew on the client (>5 min off NTP).
  `sudo timedatectl set-ntp true` on the client.
- `RATE_PRESSURE` from the replay store — the client is making
  enough requests to threaten the dedup ring eviction TTL. Investigate
  the client's send rate; usually a misconfigured loop, not a
  BeaconGate bug.

### `RESET POLICY_DENIED` for a legitimate destination

A policy rule is matching when it shouldn't.

```sh
# find which rule matched the most recent denial
journalctl -u beacongate-server -n 200 | grep tunnel.policy_denied
# the log line includes "rule_id" — that's the rule to inspect
beacongate-admin list-rules --addr http://127.0.0.1:9090 | jq '.[] | select(.id=="<rule-id>")'
```

To unblock: add a more-specific allow rule **above** the matching
rule. See [policy.md](policy.md) §"Adding a rule safely."

### Server refuses to start

```sh
# systemd:
journalctl -u beacongate-server -n 50

# docker:
docker compose -f ops/docker/docker-compose.yml logs --tail=50 beacongate-server
```

Common errors:

- `config: invalid base64 key` — your `key` field isn't a valid
  base64 string. Regenerate with `beacongate-admin gen-key`.
- `bind: address already in use` — port 8080 (or your `listen_addr`)
  is taken. `ss -tlnp | grep :8080` to find what's holding it.
- `permission denied` writing the policy store — `chown beacongate:beacongate /var/lib/beacongate`.

### Client connects locally but tunnel is unhealthy

The SOCKS5 listener is up (so `verify.sh` Step 1's local connect
works) but traffic doesn't actually exit through the server.

```sh
# 1. confirm a session is opening on the server side:
journalctl -u beacongate-server -f | grep session.open
# while you make a curl through SOCKS5

# 2. if no session.open appears: client→server transport is broken.
#    check transport.healthy + probe.ok in /api/status above.

# 3. if session.open appears but traffic doesn't flow:
#    likely a server-side dial failure (SSRF guard, DNS, or upstream
#    network) — look for tunnel.dial_failed events.
```

### `RATE_PRESSURE` from the replay store

The server's replay-defense ring buffer is filling faster than its
TTL would evict entries naturally. The replay store fails closed —
returns `RATE_PRESSURE` rather than silently dropping replay
protection.

If you see this for a *legitimate* high-throughput client:

- Increase the `replay.ring_size` in `server_config.json` (default
  60_000 entries / 10min TTL).
- If many concurrent clients legitimately need this throughput,
  consider sharding to multiple BeaconGate servers.

If you see this without explanation, suspect a misbehaving client
loop — a client retrying every request 1000x/sec will trip this.

---

## AEAD key rotation

To rotate the shared key without dropping all live sessions in the
same instant:

```sh
# 1. generate a new key
NEW_KEY=$(beacongate-admin gen-key)

# 2. update the server config
sudo sed -i.bak "s|\"key\": \".*\"|\"key\": \"$NEW_KEY\"|" /etc/beacongate/server_config.json

# 3. restart the server
sudo systemctl restart beacongate-server

# 4. all existing clients will now fail with auth_failed.
#    rebuild and redistribute bundles:
make build && make build-android
ops/prepare-bundle.sh --binary bin/beacongate-client-android-arm64 \
  --config client_config.json --out /tmp/bundle.zip --vps-ip <vps-ip>
# distribute to friends; their old bundles stop working immediately.
```

There is no graceful rotation in v1.1 — the wire envelope is keyed
to a single AEAD key per server. Plan rotation for a maintenance
window.

---

## When to escalate to logs vs metrics

Phase 1 ships structured `slog` output to stderr. Useful one-liners:

```sh
# all auth failures in last 1000 lines (potential brute-force)
journalctl -u beacongate-server -n 1000 | grep tunnel.auth_failed

# session counts over time
journalctl -u beacongate-server | grep session.open | wc -l

# everything related to a specific client_id
journalctl -u beacongate-server -o cat | grep '"client_id":"client-laptop-mr"'
```

For richer observability (Prometheus, dashboards) — see Phase 2;
Phase 1 deliberately ships only the journal-based path because
Phase-1 deployment is "one operator, one VPS."

---

## Related runbooks

- [docs/android-termux.md](android-termux.md) — phone-side issues
- [docs/operator-handoff-checklist.md](operator-handoff-checklist.md) — pre-handoff verification
- [docs/policy.md](policy.md) — adding/auditing policy rules
- [docs/deployment.md](deployment.md) — initial deployment + transport setup
- [SECURITY.md](../SECURITY.md) — vulnerability reporting + residual-risk model
