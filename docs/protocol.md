# BeaconGate Protocol Specification

## Status

Version: `1.1` — current. Hard-cuts from `1.0`; v1.0 envelopes are no longer accepted.

This document defines the BeaconGate protocol messages and envelope format carried inside an authenticated encrypted transport batch. It is the normative source for runtime protocol behavior.

The transport layer carries opaque encrypted bytes. After AEAD decryption, the plaintext batch MUST decode into exactly one BeaconGate envelope as defined here.

## Design Goals

- Make protocol meaning explicit on the wire.
- Keep transport concerns outside session semantics.
- Make malformed input handling deterministic.
- Define session behavior before runtime code exists.
- (v1.1) Make the wire envelope replay-resistant and cryptographically isolated per client.

## Version Model

BeaconGate carries **two** version concepts, deliberately separate (see plan B6):

| Concept | Bytes | Where | Purpose |
| --- | --- | --- | --- |
| **Wire-envelope version** | 1 byte cleartext, value `0x01` for v1.1 | First byte of every wire packet, BEFORE the AEAD nonce | Describes the OUTER envelope layout (header shape, AEAD scheme, replay-id placement, per-client key derivation). Bumped only when the wire layout changes. |
| **Application-protocol version** | `version.major.minor` (uint16 each) inside the JSON envelope | Inside the AEAD plaintext | Describes message-level semantics (message types, session lifecycle). Bumped per the rules below. |

Decoupling rule: a wire-envelope bump MAY ship without an application-protocol bump, and vice versa. They happen to advance together for v1.1 because per-client key derivation (wire change) and replay protection (wire change) both required new outer-envelope fields.

Application-protocol version compatibility rules:

- A sender MUST set both `major` and `minor` in every envelope.
- A receiver MUST reject any envelope whose version is not explicitly supported.
- A receiver MUST NOT assume forward compatibility with a higher minor version unless that exact version was advertised as supported.
- A major version change is always incompatible.

v1.1 implementation rules:

- Clients and servers MUST support `1.1`.
- v1.0 is **rejected** (hard cut). The `0x01` wire-version byte and `1.1` JSON `version.minor` together identify v1.1; any other combination MUST be rejected.

## Wire Envelope (v1.1)

The wire layout, before transport encoding (e.g. base64 for the `appsscript` transport):

```
[ 1 byte:  wire version = 0x01 ]
[ 2 bytes: BE client_id length N (max 1024) ]
[ N bytes: client_id (UTF-8, no NUL) ]
[ 12 bytes: AEAD nonce (random per Seal call) ]
[ rest:   AEAD ciphertext + 16-byte tag ]
```

AEAD parameters:

- Algorithm: AES-256-GCM
- Key: HKDF-SHA256 derived per-client. `key = HKDF(master_key, salt="beacongate v1.1 per-client", info=client_id, 32 bytes)`.
- Nonce: 12 random bytes per Seal.
- AAD: `wire_version || client_id_length_be || client_id` — binds the cleartext header so a captured packet posted with a swapped cleartext client_id fails authentication.
- Plaintext: `[ 8 bytes BE timestamp_ms ] || [ 16 bytes replay_id ] || [ JSON envelope ]`.

The outer-envelope properties this layout buys:

- **Per-client key isolation.** Compromise of one client's derived key does not expose other clients' traffic. The master key is still operator-shared, but cryptographic isolation between distinct `client_id`s is now real.
- **AAD-bound client_id.** A captured wire packet whose cleartext client_id is rewritten fails AEAD authentication. The server can derive the correct per-client key from the cleartext header without trusting it for content authentication.
- **Replay protection.** The 8-byte millisecond timestamp + 16-byte random replay-id combine to make every Seal call produce wire bytes that can be deduplicated server-side. The server-side replay store enforces a ±5min skew window on the timestamp and a 10-minute dedup window on the replay-id.
- **Idempotent retry.** Within a 60-second response-cache TTL, re-POSTing the exact same wire bytes returns the cached response — making transport-level failover (e.g. the `appsscript` transport's per-batch failover across deployments) safe under retry.

Identity guarantees this layout does NOT provide (see SECURITY.md "What the appsscript transport is and is NOT" for the operator-facing version):

- **Identity authenticity.** The `client_id` is self-asserted in cleartext. Any peer with the master key can claim any `client_id`. Per-client session caps and replay accounting are aggregated against the *claimed* identity, not the real peer.
- **Conflict detection.** Two peers honestly running the same `client_id` (operator error) or one impersonating another (master-key compromise) look identical to the server.

Per-tenant master keys are tracked as future work (`STEP-6-multitenant.md`).

## Envelope Structure

An envelope is the decrypted plaintext batch unit. It represents one batch of one or more protocol messages from one originating runtime instance.

Canonical envelope fields for `1.0`:

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `version.major` | uint16 | yes | Protocol major version |
| `version.minor` | uint16 | yes | Protocol minor version |
| `client_id` | string | yes | Stable originating instance identity used for server-side isolation |
| `transport` | string | no | Transport identifier when the deployment needs it |
| `compression` | string | yes | Compression marker for the plaintext batch; `none` in `1.0` |
| `messages` | array | yes | Ordered message list carried by this envelope |

Envelope rules:

- An envelope MUST contain at least one message.
- Messages in `messages` MUST be processed in array order.
- `client_id` MUST be non-empty.
- `client_id` identifies the runtime instance that originated the envelope, not the remote peer role in the abstract. A client-originated envelope and a server-originated envelope may therefore use different identifier namespaces.
- `compression` MUST be `none` in `1.0`. Any other value is malformed.
- `transport` is optional because protocol code must not depend on a specific transport. When present, it is informational routing context and MUST NOT change message semantics.
- Unknown envelope fields are malformed for `1.0`.

## Canonical Message Type Table

| Type | Meaning |
| --- | --- |
| OPEN | Begin session |
| DATA | Carry bytes |
| CLOSE | Graceful close |
| RESET | Abort session |
| PING | Keepalive |
| PROBE | Version/health probe |

## Message Structure

Every message MUST include a `type` field. Additional fields depend on message type.

### Shared Session Fields

The following rules apply to session-scoped messages:

- `session_id` is required for `OPEN`, `DATA`, `CLOSE`, `RESET`, and `PING`.
- `session_id` MUST be unique within the active session set for a given `client_id`.
- `session_id` has no meaning across different `client_id` values.

### OPEN

Begins a new outbound session.

Required fields:

| Field | Type | Meaning |
| --- | --- | --- |
| `session_id` | string | New session identifier |
| `target.network` | string | Outbound network, `tcp` in `1.0` |
| `target.host` | string | Requested hostname or IP |
| `target.port` | uint16 | Requested destination port |

Rules:

- `OPEN` MUST be the first session-scoped message for a new `session_id`.
- A receiver MUST reject duplicate `OPEN` for an already-live session with `RESET`.
- Server policy enforcement happens against the `target` fields before outbound dial.
- `OPEN` does not imply success acknowledgement. Failure is reported by `RESET`.

### DATA

Carries ordered application bytes for an existing session.

Required fields:

| Field | Type | Meaning |
| --- | --- | --- |
| `session_id` | string | Existing session identifier |
| `seq` | uint64 | Monotonic per-session data sequence |
| `data` | bytes | Opaque application payload |

Optional fields:

| Field | Type | Meaning |
| --- | --- | --- |
| `compressed` | bool | If true, `data` is gzip-compressed and MUST be decompressed before use |

Senders MAY compress `data` per message. The decision is per-message; small
payloads typically stay uncompressed because the gzip header would erase the
savings. Receivers MUST cap the decompressed size to a sensible bound to
defend against decompression bombs (16 MiB in the reference implementation).

Rules:

- `DATA` MUST only appear after `OPEN`.
- The first `DATA` for a session MUST use `seq = 0`.
- Each later `DATA` for the same session MUST increment `seq` by exactly `1`.
- Out-of-order, duplicate, or skipped `seq` values are protocol violations and MUST trigger `RESET`.
- Zero-length `data` is allowed but has no special meaning.

### CLOSE

Gracefully closes the sender's write side for a session.

Required fields:

| Field | Type | Meaning |
| --- | --- | --- |
| `session_id` | string | Existing session identifier |

Rules:

- `CLOSE` means the sender will send no more `DATA` for that session.
- `CLOSE` is half-close, not immediate full teardown. The receiver MAY continue sending `DATA` until it also sends `CLOSE` or `RESET`.
- After sending `CLOSE`, a peer MUST NOT send further `DATA` for that session.
- A session is fully removed only after both directions have closed or either side sends `RESET`.

### RESET

Aborts a session immediately.

Required fields:

| Field | Type | Meaning |
| --- | --- | --- |
| `session_id` | string | Session identifier |
| `code` | string | Machine-readable reset reason |

Optional fields:

| Field | Type | Meaning |
| --- | --- | --- |
| `reason` | string | Operator-facing diagnostic text |

Rules:

- `RESET` is terminal and immediate.
- After `RESET`, both peers MUST discard buffered state for the session.
- `RESET` may be sent for runtime errors, policy denial, malformed session message order, sequence violations, or unsupported session behavior.

Recommended `code` values for `1.0`:

- `INVALID_STATE`
- `SESSION_EXISTS`
- `BAD_SEQUENCE`
- `POLICY_DENIED`
- `DIAL_FAILED`
- `PEER_ERROR`

### PING

Carries keepalive traffic for an existing session.

Required fields:

| Field | Type | Meaning |
| --- | --- | --- |
| `session_id` | string | Existing session identifier |

Optional fields:

| Field | Type | Meaning |
| --- | --- | --- |
| `nonce` | string | Caller-supplied probe token for diagnostics |

Rules:

- `PING` MUST NOT change session state.
- A receiver MAY answer with its own `PING` carrying the same `nonce`.
- `PING` for an unknown session is an `INVALID_STATE` violation.

### PROBE

Checks liveness and carries pre-session protocol negotiation outside any session.

Request fields:

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `probe_id` | string | yes | Correlates request and response |
| `supported_versions` | array of version objects | no | Versions the sender can speak |

Response fields:

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `probe_id` | string | yes | Echo of request value |
| `status` | string | yes | `ok` or `degraded` |
| `supported_versions` | array of version objects | yes | Versions the responder supports |
| `selected_version` | version object | no | Highest common version, if one exists |

Rules:

- `PROBE` MUST NOT include `session_id`.
- `PROBE` MUST be accepted before any session is opened.
- `PROBE` is the only protocol message used for pre-session version discovery and negotiation in `1.0`.
- A `PROBE` response is a `PROBE` message, not a separate message type.

## Session Lifecycle

The persistent runtime session states for `1.0` are:

1. `idle`: no session state exists
2. `open`: `OPEN` accepted, session may exchange `DATA`, `PING`, `CLOSE`, or `RESET`
3. `half-closed-local`: local side has sent `CLOSE`
4. `half-closed-remote`: remote side has sent `CLOSE`

Lifecycle rules:

- `OPEN` transitions `idle` to `open`.
- `DATA` is valid only in `open`, `half-closed-local`, or `half-closed-remote`, subject to the rule that a side which already sent `CLOSE` cannot send more `DATA`.
- Receiving `CLOSE` transitions to the corresponding half-closed state unless the local side is already half-closed, in which case the session reaches the terminal outcome `closed` and runtime state is removed immediately.
- Any `RESET` causes the terminal outcome `reset` and runtime state is removed immediately.
- Any session-scoped message received for an unknown session MUST trigger `RESET` with `INVALID_STATE` when the receiver can identify the sender and session; otherwise the message is discarded and logged.

Terminal outcomes:

- `closed`: both directions closed cleanly; no session state remains after the close is processed
- `reset`: the session was aborted; no session state remains after the reset is processed

## Error Semantics

BeaconGate distinguishes between envelope errors and session errors.

Envelope errors:

- malformed envelope shape
- missing required envelope fields
- unsupported envelope version
- unsupported compression marker
- unknown top-level fields in `1.0`

Required behavior for envelope errors:

- The receiver MUST reject the envelope.
- The receiver MUST NOT apply any contained session mutation.
- The receiver SHOULD record a diagnostic event with the rejection reason.

Session errors:

- message order violation
- duplicate `OPEN`
- `DATA` before `OPEN`
- invalid `seq`
- policy denial
- outbound dial failure

Required behavior for session errors:

- The receiver MUST send `RESET` when it can identify the affected `session_id`.
- The receiver MUST remove session state immediately after issuing or receiving `RESET`.
- The optional `reason` field is diagnostic only and MUST NOT be parsed for control flow.

## Probe and Version Negotiation

This section defines the negotiation procedure driven by `PROBE`. The message format and field meanings are defined in the `PROBE` message section above.

Negotiation flow:

1. A peer MAY send `PROBE` before opening any session.
2. The probe sender SHOULD include `supported_versions` when it supports more than one version.
3. The responder returns its own `supported_versions`.
4. If there is at least one common version, the responder SHOULD set `selected_version` to the highest common version.
5. The sender MUST use the selected version for later envelopes.
6. If no `selected_version` is returned, the sender MUST NOT assume session compatibility and SHOULD stop before sending `OPEN`.

Health semantics:

- `status = ok` means the responder is reachable and willing to report version support.
- `status = degraded` means the responder is reachable but not healthy enough to promise normal session handling.

## Wire-Level Examples

Examples below show decrypted plaintext envelopes in JSON for readability. The transport carries these bytes only after authenticated encryption is applied. JSON field names and example byte strings such as base64 `data` values are illustrative only and do not freeze the eventual on-wire binary encoding.

### OPEN + DATA

```json
{
  "version": { "major": 1, "minor": 1 },
  "client_id": "client-alpha",
  "transport": "google",
  "compression": "none",
  "messages": [
    {
      "type": "OPEN",
      "session_id": "sess-001",
      "target": {
        "network": "tcp",
        "host": "example.com",
        "port": 443
      }
    },
    {
      "type": "DATA",
      "session_id": "sess-001",
      "seq": 0,
      "data": "AQIDBA=="
    }
  ]
}
```

### RESET

```json
{
  "version": { "major": 1, "minor": 1 },
  "client_id": "server-west-1",
  "compression": "none",
  "messages": [
    {
      "type": "RESET",
      "session_id": "sess-001",
      "code": "POLICY_DENIED",
      "reason": "destination blocked by outbound policy"
    }
  ]
}
```

### PROBE Response

```json
{
  "version": { "major": 1, "minor": 1 },
  "client_id": "server-west-1",
  "compression": "none",
  "messages": [
    {
      "type": "PROBE",
      "probe_id": "probe-42",
      "status": "ok",
      "supported_versions": [
        { "major": 1, "minor": 1 }
      ],
      "selected_version": { "major": 1, "minor": 1 }
    }
  ]
}
```

## Future Compatibility Notes

These items are planned for a future protocol bump (1.2 or 2.0); they do
not change the 1.1 wire format defined above:

- **Per-tenant master keys.** v1.1 derives per-client AEAD keys from one
  operator-shared master via HKDF. A future revision may provision one
  master per peer at install time, removing the residual self-asserted-
  identity issue (see "What the v1.1 wire format does NOT provide").
  Tracked as `STEP-6-multitenant.md`.
- **Client-side asymmetric identity.** Per-client Ed25519 keypairs with a
  signature in the cleartext header would prove identity authenticity,
  not just isolate keys. Stronger but adds complexity. Park.
- **Binary inner envelope.** The JSON envelope inside the AEAD is heavy
  on chatty traffic; a future bump could replace it with a tighter
  binary frame for higher wire density. Independent of the
  outer-envelope wire-version byte.

## Implementation Traceability

Runtime code MUST be explainable in terms of this document:

- wire-envelope version byte parsing → "Wire Envelope (v1.1)" (engine/crypto/envelope.go)
- per-client key derivation → "Wire Envelope (v1.1)" (engine/crypto/envelope.go derivePerClientAEAD)
- AAD binding → "Wire Envelope (v1.1)" (engine/crypto/envelope.go buildAAD)
- timestamp + replay-id checks → "Wire Envelope (v1.1)" (engine/replay/store.go)
- application-protocol version checks → "Version Model" (engine/protocol/version.go)
- decode validation → "Envelope Structure" + "Message Structure" (engine/protocol/decode.go)
- session state transitions → "Session Lifecycle" (server/runtime/session.go, client/runtime/sessions.go)
- protocol resets → "Error Semantics"
- pre-session compatibility checks → "Probe and Version Negotiation"

Any runtime behavior that cannot be traced back to one of those sections should be treated as unspecified and reviewed before implementation.
