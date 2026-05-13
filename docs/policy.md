# BeaconGate Server Policy

Server-side policy decides which outbound destinations the relay is
willing to dial. Decisions are made on the server, before any TCP
connection to the requested target.

## Goals

- Reduce abuse without depending on client-side cooperation.
- Give operators a structured, auditable surface to manage what the
  relay reaches.
- Keep policy data separate from policy code so updates are reviewable.

## Limits

Policy is a control layer, not a perfect content filter. It can refuse
to dial known abuse-prone hosts, but it cannot guarantee that all
illegal or harmful traffic is blocked.

## Rule shape

Each rule is a JSON object:

| Field | Required | Notes |
| --- | --- | --- |
| `id` | yes | Stable rule identifier |
| `name` | no | Human-readable label |
| `category` | no | Free-form tag (e.g. `torrent`) |
| `source` | no | `baseline`, `operator`, etc. |
| `enabled` | yes | Disabled rules are ignored |
| `action` | yes | `allow`, `block`, or `log-only` |
| `match` | yes | `exact-host`, `wildcard-host`, `exact-ip`, `cidr` |
| `pattern` | yes | The matcher input |
| `port` | no | Restrict the rule to a single port (0 = any) |
| `reason` | no | Surfaced in RESET diagnostics |
| `updated_at` | no | Set automatically by the admin API |

## Evaluation order

Rules are evaluated in order. The first applicable allow or block rule
wins. `log-only` matches do not change the decision; place them where
visibility matters. An empty rule list defaults to allow.

To express *"allow corp.example, block everything else under
*.example"* set:

```json
[
  { "id": "allow-corp", "action": "allow", "match": "exact-host",   "pattern": "corp.example",  "enabled": true },
  { "id": "block-rest", "action": "block", "match": "wildcard-host", "pattern": "*.example",     "enabled": true }
]
```

## Baseline

`policy.Baseline()` and `ops/policy/default-policy.json` carry a small
default block list focused on torrent-related categories. The intent
is conservative: catch obvious abuse vectors without claiming to be a
complete filter. Operators are expected to add environment-specific
rules on top.

## Auditability

- Every blocked decision returns a `RESET` with `code=POLICY_DENIED`
  and the rule's `reason` so operators can correlate denials with
  rules.
- The admin API rewrites `updated_at` on every successful PUT/POST so
  stored rules carry their own provenance.

## Persistence

Rules are stored as a JSON array in `policy.store_path`. Replacement is
atomic (write tmp, rename) so partially-written stores never load.
Reloading from the store happens on every successful admin write so
operators do not have to restart the server to apply changes.

---

## Adding a rule safely

The single biggest policy footgun is a wildcard rule that matches more
than you intended (e.g. `*.example.com` accidentally blocking
`api.example.com` you actually need). Defuse this with a two-step
pattern:

1. **Test first with `log-only`.** Add the rule with
   `"action": "log-only"`, watch the server logs for what would have
   matched, confirm it's only the destinations you intended:

   ```sh
   beacongate-admin put-rule --addr http://127.0.0.1:9090 --file /tmp/test-rule.json
   journalctl -u beacongate-server -f | grep policy.log_only
   ```

2. **Promote to `block`** once you're confident:

   ```sh
   sed -i 's/"log-only"/"block"/' /tmp/test-rule.json
   beacongate-admin put-rule --addr http://127.0.0.1:9090 --file /tmp/test-rule.json
   ```

For broad CIDR or wildcard rules, **always** narrow with a parallel
allow-rule for known-good destinations placed *above* the broad block:

```json
[
  { "id": "allow-our-api",  "action": "allow", "match": "exact-host",   "pattern": "api.us.example.com",  "enabled": true },
  { "id": "allow-our-corp", "action": "allow", "match": "exact-host",   "pattern": "corp.example.com",    "enabled": true },
  { "id": "block-example",  "action": "block", "match": "wildcard-host", "pattern": "*.example.com",       "enabled": true }
]
```

## Verifying which rule blocked traffic

When a user reports "I can't reach $SITE," correlate the
`POLICY_DENIED` reset with the rule that matched:

```sh
# 1. find the most recent denial for that destination
journalctl -u beacongate-server -n 1000 -o cat | \
  grep tunnel.policy_denied | \
  grep -F "$SITE"
# the log line includes "rule_id" — that's the rule that matched

# 2. inspect the rule
beacongate-admin list-rules --addr http://127.0.0.1:9090 | \
  jq '.[] | select(.id=="<rule-id>")'

# 3. add an explicit allow above it (see "Adding a rule safely")
```

## Backup and restore

The policy store is a single JSON file at `policy.store_path`. Backups
are trivial:

```sh
# back up before a risky change
sudo cp /var/lib/beacongate/policy.json /var/lib/beacongate/policy.$(date +%F).bak

# restore from backup (atomic — replace + restart so the server picks
# up the file)
sudo cp /var/lib/beacongate/policy.2026-04-15.bak /var/lib/beacongate/policy.json
sudo systemctl restart beacongate-server
```

`policy.json` is text, so `git diff` between backups is a cheap audit
trail. For remote or team operations, putting the file in a git repo
and rsync'ing it onto the server is a perfectly reasonable workflow
before moving to the admin API.

## Local-admin vs token-auth admin

The admin API listener has two modes set in `server_config.json`:

| Mode | Config | When to use |
| --- | --- | --- |
| **Loopback-only** (default) | `admin.listen_addr: "127.0.0.1:9090"` and `admin.token: ""` | One operator who SSHs to the server and runs `beacongate-admin` from there. Single-machine. Simplest. |
| **Bearer-token, public-listener** | `admin.listen_addr: ":9090"` (or any non-loopback) and `admin.token: "<long-random>"` | Multi-operator or remote admin. Requires a strong token (≥32 chars random). The token grants full policy mutation. |

The auth path is **rate-limited** (8 failures / 5 min / IP) but
otherwise has no protection beyond the bearer token. Don't expose the
admin port to the public internet without a strong token; do not put
the same token in version control. If you suspect leakage, change
`admin.token` and restart — every admin client must re-fetch the
token out-of-band.

Recommended default: loopback-only. Move to bearer-token only when a
second operator joins or remote admin is genuinely needed.
