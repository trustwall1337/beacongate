# BeaconGate Deployment Guide

This guide covers a first-pass deployment of `beacongate-server` and the
matching client. It assumes a single-server topology; multi-server
deployments are out of scope for the initial release.

## Prerequisites

- A Linux VPS reachable from the public internet, or a managed runtime
  capable of accepting HTTPS POST requests.
- Go 1.25+ if you intend to build from source. Otherwise grab a release
  binary.
- A TLS-terminating reverse proxy (Caddy, Nginx, or a managed load
  balancer). The server itself speaks plain HTTP and expects to be
  fronted with TLS.

## 1. Generate a shared key

The client and server share a 32-byte AEAD key, base64-encoded:

```sh
beacongate-admin gen-key
```

Save the result. Both sides use the same value.

## 2. Configure the server

Edit `server_config.json`. Minimal example:

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
- `admin.token` is empty for loopback-only mode. Set a long random
  string to expose the admin API on a non-loopback address.
- `store_path` should be on persistent storage. The systemd unit
  reserves `/var/lib/beacongate` for this purpose.

## 3. Run the server

### systemd

Copy `ops/systemd/beacongate-server.service` to
`/etc/systemd/system/beacongate-server.service`, then:

```sh
useradd --system --home /var/lib/beacongate --shell /usr/sbin/nologin beacongate
install -d -m 750 -o beacongate -g beacongate /var/lib/beacongate
install -d -m 750 /etc/beacongate
install -m 640 -o beacongate -g beacongate server_config.json /etc/beacongate/server_config.json
systemctl daemon-reload
systemctl enable --now beacongate-server
systemctl status beacongate-server
```

### Docker Compose

```sh
cd ops/docker
docker compose up -d
docker compose logs -f beacongate-server
```

Place `server_config.json` in `ops/docker/config/` before starting.

## 4. Front the server with TLS

Example Caddy fragment:

```caddy
relay.example.com {
  reverse_proxy /tunnel localhost:8080
  reverse_proxy /healthz localhost:8080
}
```

The path served on the public internet must match `tunnel_path` in the
server config.

## 5. Configure the client

Edit `client_config.json`:

```json
{
  "client_id": "client-laptop-mr",
  "listen_addr": "127.0.0.1:1080",
  "server": {
    "url": "https://relay.example.com/tunnel",
    "key": "BASE64_KEY"
  },
  "transport": {
    "type": "google",
    "options": {
      "fronting_host": "",
      "health_url": "https://relay.example.com/healthz"
    }
  }
}
```

Then start the client:

```sh
beacongate-client -config client_config.json -control-addr 127.0.0.1:9091
```

Local apps point their SOCKS5 setting at `127.0.0.1:1080`.

## 6. Verify

```sh
curl -fsS https://relay.example.com/healthz       # returns "ok"
beacongate-admin status --addr http://127.0.0.1:9090
curl -x socks5h://127.0.0.1:1080 https://example.com
```

If `beacongate-client` exits at startup, check the diagnostics line in
its log: it reports transport health and probe outcome separately so a
TLS issue, a wrong key, or a version mismatch each surface distinctly.

## 7. Updating policy

Use the admin API or `beacongate-admin`:

```sh
beacongate-admin put-rule --addr http://127.0.0.1:9090 --file rule.json
beacongate-admin list-rules --addr http://127.0.0.1:9090
beacongate-admin delete-rule --addr http://127.0.0.1:9090 --id my-rule
```

Every successful change reloads the in-memory engine; new sessions see
the updated rules without restarting the server.

## 8. Recovery

| Symptom | First step |
| --- | --- |
| `auth failed` (HTTP 401) on `/tunnel` | Server and client keys diverged. Regenerate and roll. |
| `transport.healthy=false` at startup | DNS or TLS issue between client and reverse proxy. |
| `probe.ok=false` but transport healthy | Endpoint reachable but server rejects envelope. Check protocol version. |
| `RESET POLICY_DENIED` for legitimate target | Add an explicit allow rule above the matching block. |
| Server refuses to start | `journalctl -u beacongate-server` for systemd, `docker compose logs` for Docker. |
