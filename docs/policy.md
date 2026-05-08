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
