# BeaconGate Admin API

Local administrative HTTP surface for managing server outbound policy.
Designed to run on the same host as the server, optionally exposed
remotely via a bearer token.

## Authentication

| Mode | Listen address | Auth |
| --- | --- | --- |
| `local` | loopback only | none (loopback peer check) |
| `remote` | any | `Authorization: Bearer <token>` |

Local mode rejects any non-loopback peer with HTTP 401. Remote mode
requires the bearer token configured in `admin.token`.

## Endpoints

### `GET /api/status`

Returns runtime status:

```json
{ "session_count": 3 }
```

### `GET /api/policy/rules`

List all rules:

```json
[
  { "id": "r1", "action": "block", "match": "wildcard-host", "pattern": "*.example.com", "enabled": true }
]
```

### `POST /api/policy/rules`

Create or upsert a rule. Body is a single rule object:

```json
{
  "id": "block-bad-tracker",
  "name": "Bad tracker",
  "category": "torrent",
  "action": "block",
  "match": "wildcard-host",
  "pattern": "*.bad-tracker.example",
  "port": 0,
  "enabled": true,
  "reason": "abuse"
}
```

Returns 201 with the stored rule.

### `GET /api/policy/rules/{id}`

Return a single rule by id, or 404.

### `PUT /api/policy/rules/{id}`

Update a rule. Body is the full rule. The path id wins over any body id.
Returns 200 with the stored rule.

### `DELETE /api/policy/rules/{id}`

Delete a rule. Returns 204 on success, 404 if missing.

## Match kinds

| `match` | meaning |
| --- | --- |
| `exact-host` | exact case-insensitive hostname match |
| `wildcard-host` | `*.example.com` style suffix |
| `exact-ip` | match a single IP literal |
| `cidr` | match within CIDR |

## Actions

| Action | Effect |
| --- | --- |
| `allow` | explicit allow (overrides later block rules) |
| `block` | refuse the dial; client receives `RESET POLICY_DENIED` |
| `log-only` | record the match without changing the decision |

## Reload semantics

Every successful POST/PUT/DELETE refreshes the in-memory policy engine
from the store, so rule changes take effect on the next session OPEN
without restarting the server.

## Rate limits (v1.1)

The server enforces two rate limits, both internal to the runtime
(neither is configurable through the admin API today; both have
hard-coded defaults):

| Limit | Default | Returns |
| --- | --- | --- |
| Admin auth failures per IP | 8 / 5 min | HTTP 429 with `Retry-After` |
| `/tunnel` requests per IP | 50 req/s, burst 100 | HTTP 429 |

A peer that bursts past the `/tunnel` cap is throttled per source IP;
sustained overage indicates either a misbehaving client or a flood
from someone holding the AEAD key. Pair with the per-client session
cap (default 100, configurable in `server_config.json` under
`limits.max_sessions_per_client`) and the v1.1 replay store's
RATE_PRESSURE rejection for defense in depth.
