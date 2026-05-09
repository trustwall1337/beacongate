# BeaconGate server in Docker

Run the BeaconGate relay server as a container. Designed for a single-host
deployment behind a TLS-terminating reverse proxy (Caddy, nginx,
Cloudflare). The client and the admin CLI run on user laptops, not in
this container.

## One-shot start (from the repo root)

```sh
make docker-up
```

That target:

1. Compiles `beacongate-admin` locally (so it can mint a key).
2. If `ops/docker/config/server_config.json` doesn't exist, generates a
   fresh AEAD key and renders the config from
   `ops/docker/server_config.template.json`.
3. `docker compose build` and `docker compose up -d`.
4. Polls the container's healthcheck until it reports healthy.

After `docker-up`:

```sh
make docker-logs       # tail server logs
make docker-status     # compose ps + healthz reachability from host
make docker-down       # stop (config and policy store preserved)
make docker-clean      # stop + delete the named volume (policy store gone)
```

## What's where

| Path | Role |
| --- | --- |
| [Dockerfile.server](Dockerfile.server) | Two-stage build of `beacongate-server` and `beacongate-admin` on `alpine:3.20`. Drops capabilities, runs as uid 10001. |
| [docker-compose.yml](docker-compose.yml) | Single service, restart-unless-stopped, bind-mount config, named volume for policy store, healthcheck, capability drop. |
| [server_config.template.json](server_config.template.json) | Skeleton config used by `make docker-init`. `KEY_PLACEHOLDER` is replaced with a freshly generated AEAD key. |
| `config/` | Bind-mounted into the container at `/etc/beacongate`. The live `server_config.json` lives here and is **gitignored** (never commit a key). |

## Networking

| Port | Where | Purpose |
| --- | --- | --- |
| `8080` | published to all interfaces | Tunnel endpoint. **Front with TLS in production.** |
| `9090` | not published by default | Admin API. See "Enabling the admin API" below. |

The admin API is disabled by default in the docker config because the
server's local-only auth check (`IsLoopback`) **does not work** through
Docker's port forwarding — the bridge interface IP is not loopback from
the container's perspective. To expose admin, you must use bearer-token
mode.

## Enabling the admin API

Edit `config/server_config.json`:

```json
"admin": {
  "enabled": true,
  "listen_addr": ":9090",
  "token": "<paste a long random token here>"
}
```

Generate a token with any of:

```sh
openssl rand -base64 32
head -c 32 /dev/urandom | base64
```

Then in `docker-compose.yml`, uncomment the `9090` port mapping:

```yaml
ports:
  - "8080:8080"
  - "127.0.0.1:9090:9090"
```

`docker compose up -d` (or `make docker-up` again) to apply. Token-protected
admin requests:

```sh
curl -H "Authorization: Bearer <token>" http://127.0.0.1:9090/api/status
```

## Production checklist

- [ ] TLS termination in front of `:8080` (the tunnel endpoint).
- [ ] DNS pointing at the host.
- [ ] Backups of the `beacongate-state` volume (the policy store survives
      restarts but `make docker-clean` deletes it).
- [ ] If you publish admin, change `127.0.0.1:9090:9090` to whatever you
      actually want reachable, and never use a weak token.
- [ ] Resource limits in `docker-compose.yml` tuned for your host
      (defaults: 256MB mem, 256 pids).
- [ ] Log shipping: the container writes structured logs to stderr.
      `docker compose logs -f` for ad-hoc; pair with a logging driver
      (loki, journald, gcplogs, awslogs) for retention.

## Troubleshooting

**`make docker-up` says config exists, won't overwrite.**
Intentional. Delete `ops/docker/config/server_config.json` if you want a
fresh key (will invalidate every existing client).

**Container is unhealthy after start.**
```sh
make docker-logs
```
Look for `auth_failed` (key mismatch with client), `bad_envelope`
(version skew), or `http.serve_failed` (port conflict on the host).

**Container exits immediately with `key:` error.**
The config file is missing or `KEY_PLACEHOLDER` is still literally in
there. Re-run `make docker-init`.

**Policy not persisting across restarts.**
Make sure `policy.store_path` in your config is under `/var/lib/beacongate`,
which is the named volume. Default template already points there.

**Admin API rejects requests with 401 even with the right token.**
Check that `admin.token` in the config matches the `Authorization: Bearer
…` header byte-for-byte. The auth code uses constant-time compare.
