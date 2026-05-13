# Protocol (cross-language)

This directory is the canonical, language-neutral home for the BeaconGate
wire protocol. Today it contains pointers to existing artifacts; as more
languages join the monorepo, machine-readable schemas (JSON Schema, IDL,
or `.proto` files) belong here so every implementation generates types
from a single source.

## What lives here

| Path | Contents |
| --- | --- |
| `../docs/protocol.md` | Human-readable specification (v1.1, normative). |
| `schemas/` *(future)* | Machine-readable schemas — likely JSON Schema for the envelope and per-message shapes. |
| `idl/` *(future)* | If we adopt an IDL (proto, flatbuffers, capnp), source files live here. |
| `vectors/` *(future)* | Cross-language test vectors: known-good envelopes, ciphertexts, and edge cases. |

The Go reference implementation in [../engine/protocol/](../engine/protocol/)
remains the working source of truth until schemas exist.

## Why a top-level directory

Language-specific implementations (Go in `engine/`, future clients in
`desktop/` and `mobile/`) all bind to the protocol. Anchoring the spec at
the repo root rather than inside any language subtree makes the
neutrality explicit.
