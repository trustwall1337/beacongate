# BeaconGate Deployment Guide

This guide covers deploying `beacongate-server` and a matching client.
There are **two** distinct deployment shapes — pick one before reading
further. Mixing them is a configuration error.

## Pick a transport mode

The two transports are mutually exclusive. The full contract for each
is below — read this table once, then jump to Playbook A or B.

| Property | `https` (Playbook A) | `appsscript` (Playbook B) |
| --- | --- | --- |
| **Use case** | Self-hosted relay where the operator controls the network path (own domain, CDN, TLS cert). NOT a censorship-evasion path on its own. | Censorship-evasion: end-user traffic must look like ordinary Google traffic. |
| **TCP destination** | The hostname in `server.url`, resolved via DNS | One of the configured `google_host` IPs (default `216.239.38.120:443`) |
| **TLS SNI** | `server.url`'s hostname (or operator's CDN if fronted externally) | One of the configured `sni` rotation hostnames (default `www.google.com`) |
| **HTTP `Host` header** | `server.url`'s hostname (or `FrontingHost` override) | `script.google.com` (set automatically by Go's stdlib once URL is `script.google.com`) |
| **HTTP target path** | The operator-chosen `tunnel_path` on the BeaconGate server (default `/tunnel`) | `/macros/s/{DEPLOYMENT_ID}/exec` on `script.google.com` |
| **Body encoding** | Binary, `Content-Type: application/octet-stream` | Base64 standard alphabet **with padding**, `Content-Type: text/plain`. The Apps Script `Code.gs` decodes on the way in and re-encodes on the way out — the BeaconGate VPS server stays binary-only. |
| **Forwarder hop** | None (direct) | Apps Script `Code.gs` running in the operator's Google account |
| **Daily quota** | None (operator-imposed only) | ~20K UrlFetchApp invocations/day per Google account; rotated across multiple `script_keys` |
| **Failover** | None at the transport layer | Single-attempt per-batch failover across `script_keys` with exponential backoff, max **2 attempts per batch** |
| **Required client config** | `server.url` (full HTTPS URL), optional `transport.options.fronting_host` | `transport.options.script_keys` (comma-separated deployment IDs). **`server.url` MUST be omitted.** |
| **Required server-side setup** | TLS-terminating reverse proxy in front of BeaconGate server | Apps Script web app (`Code.gs`) deployed in the operator's Google account, pointing `RELAY_URL` at the BeaconGate VPS |
| **Threat model** | "Operator-controlled relay; no on-path-censor evasion." A passive DPI sees TLS to `relay.your-domain.com` — easy to identify. | Network-layer DPI sees TLS to a Google IP with `SNI=www.google.com` and HTTP `Host: script.google.com`. Blocking requires also blocking `script.google.com`, with collateral damage to legitimate Apps Script users. **Residual risks: traffic-pattern analysis, TLS-fingerprint analysis, Google's own classifiers, URL-pattern blocking of `/macros/s/.../exec`. Raises the cost of blocking; does not make blocking impossible.** |
| **`Validate()` rejects** | `transport.type=https` without `server.url` | `transport.type=appsscript` with non-empty `server.url`; `transport.type=appsscript` without `script_keys` |

Both modes share the same BeaconGate VPS server image; the server has
no transport-mode awareness — it sees opaque sealed bytes either way.

The two playbooks below are **independent**. Read only the one that
matches your chosen mode. Don't try to combine fields across them —
the config loader's `Validate()` will reject the result.

---

# Playbook A: Direct HTTPS deployment (`https` mode)

For operators who run their own relay behind their own TLS endpoint.
This deployment does NOT make the traffic look like Google; deploy
behind a CDN with domain fronting if you need that property without
the Apps Script hop.

## A1. Prerequisites

- A Linux VPS reachable from the public internet, or a managed runtime
  capable of accepting HTTPS POST requests.
- A TLS-terminating reverse proxy (Caddy, Nginx, or a managed load
  balancer). The server itself speaks plain HTTP and expects to be
  fronted with TLS.
- A DNS hostname pointing at the VPS (e.g. `relay.your-domain.com`).
- A release binary (recommended) or Go 1.25+ to build from source.

## A1b. Get the binaries (recommended path)

Download the latest release for your VPS architecture from
<https://github.com/trustwall1337/beacongate/releases>. Each release
ships:

- `BeaconGate-vX.Y.Z-linux-{amd64,arm64}.tar.gz` (and macOS, Windows, Android variants)
- `BeaconGate-vX.Y.Z-checksums.txt` — SHA-256 of every archive
- `BeaconGate-vX.Y.Z-checksums.txt.sig` + `.cert` — cosign signature

**Verify the cosign signature before extracting** (sigstore + GitHub
OIDC; no operator key management):

```sh
# Install cosign (one-time):
# Linux/macOS: brew install cosign  -or-  go install github.com/sigstore/cosign/v2/cmd/cosign@latest

# Verify checksums.txt was signed by this repo's release workflow:
cosign verify-blob \
  --certificate BeaconGate-vX.Y.Z-checksums.txt.cert \
  --signature  BeaconGate-vX.Y.Z-checksums.txt.sig \
  --certificate-identity-regexp 'https://github.com/trustwall1337/beacongate/.+' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  BeaconGate-vX.Y.Z-checksums.txt

# Then verify your tarball matches:
sha256sum -c BeaconGate-vX.Y.Z-checksums.txt --ignore-missing

# Extract:
tar -xzf BeaconGate-vX.Y.Z-linux-amd64.tar.gz
sudo install -m755 beacongate-{client,server,admin} /usr/local/bin/
```

If `cosign verify-blob` fails, **stop**: the artifact has been
tampered with or the signing identity has changed. Don't extract.

The fast-track install script (`curl -fsSL …/install.sh | bash`)
runs the same verification before unpacking — see `scripts/install.sh`.

## A2. Generate a shared key

```sh
beacongate-admin gen-key
```

Save the base64 string it prints. Both client and server use the same
value.

## A3. Configure and run the server

Edit `server_config.json`:

```json
{
  "server_id": "server-west-1",
  "listen_addr": ":8080",
  "tunnel_path": "/tunnel",
  "health_path": "/healthz",
  "key": "BASE64_KEY",
  "policy": {
    "baseline_enabled": true,
    "store_path": "/var/lib/beacongate/policy.json"
  },
  "admin": {
    "enabled": true,
    "listen_addr": "127.0.0.1:9090",
    "token": ""
  }
}
```

Notes:

- `policy.baseline_enabled` loads the bundled abuse-prone block list.
- `admin.token` empty = loopback-only admin. Set a long random string
  to expose the admin API on a non-loopback address.
- `store_path` should be on persistent storage. The systemd unit
  reserves `/var/lib/beacongate` for this purpose.

Start it:

```sh
# systemd
useradd --system --home /var/lib/beacongate --shell /usr/sbin/nologin beacongate
install -d -m 750 -o beacongate -g beacongate /var/lib/beacongate
install -d -m 750 /etc/beacongate
install -m 640 -o beacongate -g beacongate server_config.json /etc/beacongate/server_config.json
systemctl daemon-reload
systemctl enable --now beacongate-server
systemctl status beacongate-server

# or Docker Compose
cd ops/docker && docker compose up -d
docker compose logs -f beacongate-server
```

## A4. Front the server with TLS

Example Caddy fragment:

```caddy
relay.your-domain.com {
  reverse_proxy /tunnel localhost:8080
  reverse_proxy /healthz localhost:8080
}
```

The public path must match `tunnel_path` in the server config.

## A5. Configure and run the client

Use [client_config.example.json](../client_config.example.json) as
the base; fill in your values:

```json
{
  "client_id": "client-laptop-mr",
  "listen_addr": "127.0.0.1:1080",
  "server": {
    "url": "https://relay.your-domain.com/tunnel",
    "key": "BASE64_KEY"
  },
  "transport": {
    "type": "https",
    "options": {
      "fronting_host": "",
      "health_url": "https://relay.your-domain.com/healthz"
    }
  }
}
```

Then:

```sh
beacongate-client -config client_config.json -control-addr 127.0.0.1:9091
```

Local apps point their SOCKS5 setting at `127.0.0.1:1080`.

## A6. Verify

```sh
curl -fsS https://relay.your-domain.com/healthz   # returns "ok"
curl -x socks5h://127.0.0.1:1080 https://example.com
```

### Optional: SOCKS5 auth + LAN sharing

By default the client binds to `127.0.0.1:1080` so only the local
machine can use the tunnel. For LAN-shared deployments (one client
serving multiple devices on a home network), set `listen_addr` to
`0.0.0.0:1080` AND set `socks.username` + `socks.password` — without
auth, anyone on the LAN can drain your daily Apps Script quota.

```json
{
  "listen_addr": "0.0.0.0:1080",
  "socks": {
    "username": "alice",
    "password": "long-random-shared-secret"
  },
  "server": { ... },
  "transport": { ... }
}
```

Devices on the LAN connect with
`curl -x socks5://alice:long-random-shared-secret@<lan-ip>:1080 ...`.

---

# Playbook B: Apps Script deployment (`appsscript` mode)

For operators delivering BeaconGate to filtered users. The `appsscript`
transport routes every batch through a user-deployed Google Apps
Script web app, so on the wire the client is talking to Google.

## B1. Prerequisites

- A Linux VPS reachable from the public internet (Apps Script needs to
  be able to call it via UrlFetchApp).
- A Google account, or several. Apps Script enforces a ~20,000
  invocations/day quota per account that resets at midnight Pacific.
  Multiple accounts spread the ceiling.
- A release binary (recommended) or Go 1.25+ to build from source.

## B1b. Get the binaries

Same as Playbook A1b: download the matching release tarball and
**verify the cosign signature** before extracting. See [A1b](#a1b-get-the-binaries-recommended-path)
above for the verification commands. The binaries (and the cosign
verification step) are the same regardless of which transport mode
you'll run.

> **NOTE:** Unlike Playbook A, the VPS does NOT need to be behind a
> TLS-terminating reverse proxy. The client never connects to your VPS
> directly — it connects to script.google.com. Plain HTTP between
> Apps Script and your VPS is acceptable (and sometimes necessary
> because Apps Script's UrlFetchApp can be picky about TLS chains on
> self-signed deployments). For production, putting the VPS behind a
> real TLS cert anyway is a defense-in-depth.

## B2. Generate a shared key

Same as Playbook A:

```sh
beacongate-admin gen-key
```

## B3. Configure and run the server

Identical to Playbook A's server config. The server has no
transport-mode awareness — it sees opaque sealed bytes either way.

```json
{
  "server_id": "server-west-1",
  "listen_addr": ":8080",
  "tunnel_path": "/tunnel",
  "health_path": "/healthz",
  "key": "BASE64_KEY",
  "policy": { "baseline_enabled": true, "store_path": "/var/lib/beacongate/policy.json" },
  "admin": { "enabled": true, "listen_addr": "127.0.0.1:9090", "token": "" }
}
```

Start it the same way as A3. The VPS URL you'll embed in the Apps
Script (B5) is `http://YOUR.VPS.IP:8080/tunnel` (or
`https://relay.your-domain.com/tunnel` if you also chose to front it).

## B4. Open the VPS firewall

The VPS port (default 8080) must be reachable from
`script.google.com`'s outbound IPs. The simplest path:

```sh
sudo ufw allow 8080/tcp
```

If your cloud provider has a separate firewall layer (AWS Security
Groups, GCP Firewall Rules, etc.), open the port there too.

Verify from your laptop (the BeaconGate VPS server's `/healthz` is a
fast sanity check):

```sh
curl http://YOUR.VPS.IP:8080/healthz
# → "ok"
```

## B5. Deploy the Apps Script forwarder

Read [apps_script/README.md](../apps_script/README.md) for the full
flow. Short version:

1. Open <https://script.google.com/> and create a new project.
2. Replace the default `Code.gs` with the contents of
   [apps_script/Code.gs](../apps_script/Code.gs).
3. **Edit the `RELAY_URL` constant** near the top of the script:
   ```javascript
   const RELAY_URL = 'http://YOUR.VPS.IP:8080/tunnel';
   ```
   Replace `YOUR.VPS.IP:8080` with your actual VPS host:port.
4. Deploy → New deployment → type **Web app** → Execute as **Me** →
   Who has access **Anyone** → click **Deploy**.
5. On first deploy Google asks you to authorize the script (it needs
   permission to make outbound URL fetches). Authorize it.
6. Copy the **Deployment ID** (NOT the Script ID — they look similar
   but are different).
7. Verify the deployment by hitting its `/exec` URL in a browser:
   ```
   https://script.google.com/macros/s/YOUR_DEPLOYMENT_ID/exec
   ```
   Expected: a JSON blob `{"ok":true,"date":"...","count":N,...}`. If
   you get an HTML error page, redo step 5.

For each additional Google account that you want to add to the
rotation, repeat steps 1–6 in that account. Note each deployment ID.

## B6. Configure and run the client

Use [client_config.appsscript.example.json](../client_config.appsscript.example.json)
as the base; fill in your values:

```json
{
  "client_id": "client-laptop-mr",
  "listen_addr": "127.0.0.1:1080",
  "server": {
    "key": "BASE64_KEY"
  },
  "transport": {
    "type": "appsscript",
    "options": {
      "script_keys": "DEPLOYMENT_ID_1,DEPLOYMENT_ID_2",
      "script_accounts": "alpha-account,beta-account",
      "google_host": "216.239.38.120:443",
      "sni": "www.google.com,mail.google.com,accounts.google.com"
    }
  }
}
```

Field guide:

- `server.key` — the shared AEAD key (B2). **`server.url` MUST be
  omitted** in this mode (Validate() rejects the config otherwise).
- `transport.options.script_keys` — comma-separated deployment IDs
  from B5. The client round-robins across them and fails over on
  per-deployment errors (max two attempts per batch).
- `transport.options.script_accounts` — optional human-readable label
  per deployment, parallel to `script_keys`. Used in stats output to
  group quota usage by Google account.
- `transport.options.google_host` — IP:port to TCP-dial. Defaults to
  `216.239.38.120:443` (a Google edge IP). The TCP destination is
  what a passive observer sees.
- `transport.options.sni` — comma-separated SNI hostnames. Each
  becomes its own TLS session ticket cache and HTTP connection pool.
  Defaults to `www.google.com`. Multiple SNI hosts spread per-domain
  CDN throttle buckets — useful under heavy load.

Then:

```sh
beacongate-client -config client_config.json -control-addr 127.0.0.1:9091
```

Local apps point their SOCKS5 setting at `127.0.0.1:1080`.

## B7. Verify

```sh
# Local: SOCKS works end-to-end
curl -x socks5h://127.0.0.1:1080 https://example.com

# Local: client thinks transport is healthy
beacongate-client -config client_config.json -control-addr 127.0.0.1:9091
# → log line: startup diagnostics: transport.healthy=true probe.ok=true ...

# On the VPS: check that requests are arriving from script.google.com
sudo tcpdump -nn -i any port 8080
# → expect inbound from Google IPs (66.249.* or 64.233.*), NOT from your laptop's IP

# On the laptop: confirm zero direct connections to your VPS
sudo tcpdump -nn -i any host YOUR.VPS.IP
# → expect ZERO packets while traffic flows
```

The third check is the one that proves the disguise is real. If you
see direct laptop→VPS packets while `appsscript` mode is supposed to
be active, something is misconfigured.

## B8. Updating the script

Saving `Code.gs` does NOT update what your clients see. Every change
needs a new deployment:

1. Edit `Code.gs` in script.google.com
2. Deploy → New deployment (or Manage deployments → New version)
3. Copy the new deployment ID
4. Update `script_keys` in `client_config.json`
5. Restart `beacongate-client`

## B9. Quota visibility

The transport's `Diagnose()` output reports each deployment's daily
count, fetched via the `Code.gs` `doGet` endpoint every 30 minutes.
You can also hit `https://script.google.com/macros/s/{ID}/exec`
manually to see the same JSON.

The client also keeps its own per-response counter (incremented on
every HTTP response received from a deployment) which is reported as
`today=N` alongside the script-reported `script=N`. The two should
agree within a few hundred between polls; large divergence means
either a clock drift or someone else is hitting your deployment.

If a deployment hits its quota mid-day, the client puts it in a long
backoff (30 min) and uses other entries in `script_keys` until the
midnight Pacific reset.

---

## Optional: route outbound through a proxy (`upstream_proxy`)

By default the BeaconGate server dials destinations directly from
the VPS. The destination sees the VPS's datacenter IP. **Cloudflare
and other bot-defense systems score datacenter IPs heavily — many
Cloudflare-protected sites will challenge with captchas or block
outright.** Routing the VPS's outbound through a SOCKS5 proxy (e.g.
Cloudflare WARP) makes the destination see the proxy's egress IP
instead.

Set `upstream_proxy` in `server_config.json`:

```json
{
  ...,
  "upstream_proxy": "socks5://127.0.0.1:40000"
}
```

Validation: only `socks5://host:port` is accepted; other schemes are
rejected at config-load time.

### Cloudflare WARP setup (typical case)

1. Install the WARP client on your VPS:
   ```sh
   # Debian/Ubuntu
   curl https://pkg.cloudflareclient.com/pubkey.gpg | sudo gpg --yes --dearmor --output /usr/share/keyrings/cloudflare-warp-archive-keyring.gpg
   echo "deb [signed-by=/usr/share/keyrings/cloudflare-warp-archive-keyring.gpg] https://pkg.cloudflareclient.com/ $(lsb_release -cs) main" | sudo tee /etc/apt/sources.list.d/cloudflare-client.list
   sudo apt update && sudo apt install cloudflare-warp -y
   ```
2. Register and enable the proxy mode:
   ```sh
   sudo warp-cli registration new
   sudo warp-cli mode proxy
   sudo warp-cli proxy port 40000
   sudo warp-cli connect
   ```
3. Verify WARP is reachable on the local SOCKS5 port:
   ```sh
   curl -x socks5://127.0.0.1:40000 https://www.cloudflare.com/cdn-cgi/trace
   # the "ip=" line should be a Cloudflare egress IP, not your VPS IP
   ```
4. Set `upstream_proxy: "socks5://127.0.0.1:40000"` in
   `server_config.json` and restart the server.

### Trust model with `upstream_proxy`

- **SSRF guard still runs locally on the resolved destination IP.**
  The server resolves the hostname and validates the IP before
  handing the dial to the proxy. A compromised client cannot use
  the proxy to bypass our private/loopback/metadata block list.
- **DNS-via-proxy is NOT preserved.** Goose's docs claim DNS goes
  through the proxy too; ours doesn't (we resolve locally for SSRF
  enforcement). The user-visible Cloudflare-egress-IP property holds
  for TCP, but DNS leaks through the VPS's resolver. If DNS-leak
  protection matters, set the VPS resolver to a privacy-friendly
  service (`1.1.1.1`, `9.9.9.9`).
- **The proxy itself is operator-trusted.** A malicious local
  SOCKS5 proxy could redirect every dial. Run only proxies you
  control.

---

## Updating policy (both playbooks)

Use the admin API or `beacongate-admin`:

```sh
beacongate-admin put-rule --addr http://127.0.0.1:9090 --file rule.json
beacongate-admin list-rules --addr http://127.0.0.1:9090
beacongate-admin delete-rule --addr http://127.0.0.1:9090 --id my-rule
```

Every successful change reloads the in-memory engine; new sessions see
the updated rules without restarting the server.

## Recovery (both playbooks)

For an exhaustive runbook with first-response commands per failure
mode, see [troubleshooting.md](troubleshooting.md). Quick reference:

| Symptom | First step |
| --- | --- |
| `auth failed` (HTTP 401) on `/tunnel` | Server and client AEAD keys diverged. Regenerate and roll. |
| `transport.healthy=false` at startup (https mode) | DNS or TLS issue between client and reverse proxy. |
| `transport.healthy=false` at startup (appsscript mode) | Apps Script deployment unreachable. Hit `https://script.google.com/macros/s/{ID}/exec` in a browser; should return JSON. |
| `probe.ok=false` but transport healthy | Endpoint reachable but server rejects envelope. Check protocol version + key match. |
| `RESET POLICY_DENIED` for legitimate target | Add an explicit allow rule above the matching block. |
| Server refuses to start | `journalctl -u beacongate-server` for systemd, `docker compose logs` for Docker. |
| All deployments quota-blacklisted (appsscript mode) | Wait until midnight Pacific, or add more entries to `script_keys` from additional Google accounts. |

## Handing the client to an Android end-user

Phase 1 does not ship a native APK. The intended flow is: cross-compile
the client for `linux/arm64`, package a bundle for the friend, and
have them run the binary in [Termux](https://termux.dev) with NekoBox
or v2rayNG consuming the local SOCKS5 listener.

```sh
make build-android
ops/prepare-bundle.sh \
  --binary bin/beacongate-client-android-arm64 \
  --config client_config.json \
  --vps-ip $(curl -fsS https://api.ipify.org) \
  --out /tmp/beacongate-bundle.zip
```

The full operator-side procedure is in
[operator-handoff-checklist.md](operator-handoff-checklist.md); the
end-user setup is in [android-termux.md](android-termux.md).
