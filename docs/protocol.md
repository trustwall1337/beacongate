# BeaconGate Protocol Specification

## Status

Version: `1.0`

This document defines the BeaconGate protocol messages and envelope format carried inside an authenticated encrypted transport batch. It is the normative source for runtime protocol behavior in the initial implementation.

The transport layer carries opaque encrypted bytes. After decryption, the plaintext batch MUST decode into exactly one BeaconGate envelope as defined here.

## Design Goals

- Make protocol meaning explicit on the wire.
- Keep transport concerns outside session semantics.
- Make malformed input handling deterministic.
- Define session behavior before runtime code exists.

## Versioning Model

BeaconGate uses a two-part protocol version:

- `major`: incompatible wire or semantic changes
- `minor`: additive or clarifying changes within the same major line

Version `1.0` is the initial protocol defined by this document.

Compatibility rules:

- A sender MUST set both `major` and `minor` in every envelope.
- A receiver MUST reject any envelope whose version is not explicitly supported.
- A receiver MUST NOT assume forward compatibility with a higher minor version unless that exact version was advertised as supported.
- A major version change is always incompatible.
- A minor version change may be compatible in implementation terms, but the runtime MUST treat support as opt-in through explicit version advertisement.

Initial implementation rules:

- Clients and servers MUST support `1.0`.
- If version negotiation has not happened yet, a sender MUST use `1.0`.
- All envelopes in a live session MUST use the same negotiated version.

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
  "version": { "major": 1, "minor": 0 },
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
  "version": { "major": 1, "minor": 0 },
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
  "version": { "major": 1, "minor": 0 },
  "client_id": "server-west-1",
  "compression": "none",
  "messages": [
    {
      "type": "PROBE",
      "probe_id": "probe-42",
      "status": "ok",
      "supported_versions": [
        { "major": 1, "minor": 0 }
      ],
      "selected_version": { "major": 1, "minor": 0 }
    }
  ]
}
```

## Future Compatibility Notes

These items are planned for the next protocol bump (1.1 or 2.0); they do
not change the 1.0 wire format defined above:

- **Per-client key derivation.** The current envelope is fully encrypted
  and the sealing key is shared across all clients. A future revision may
  carry a small cleartext header (just `client_id`) so the server can
  derive a per-client AEAD key via HKDF before opening the envelope.
  Compromise of one client's key would then no longer expose other
  clients' traffic. Until that bump, deployments rotate the master key
  on suspected compromise.

## Implementation Traceability

Later runtime code MUST be explainable in terms of this document:

- wire version checks map to Versioning Model
- decode validation maps to Envelope Structure and Message Structure
- session state transitions map to Session Lifecycle
- protocol resets map to Error Semantics
- pre-session compatibility checks map to Probe and Version Negotiation

Any runtime behavior that cannot be traced back to one of those sections should be treated as unspecified and reviewed before implementation.
