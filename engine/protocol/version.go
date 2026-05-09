package protocol

// Two version concepts coexist in BeaconGate, deliberately separate
// (see plan B6 "Version model" / docs/protocol.md "Version Model"):
//
//  1. **Application-protocol version** — `version.major.minor` inside
//     the JSON envelope, post-AEAD-decryption. Describes message-
//     level semantics (message types, session lifecycle). Bumped per
//     the docs/protocol.md versioning model.
//
//  2. **Wire-envelope version** — a single byte at the very front of
//     every wire packet, BEFORE the AEAD nonce. Describes the OUTER
//     envelope layout (header shape, AEAD scheme, replay-id placement,
//     per-client key derivation). Bumped only when the wire layout
//     changes. The constant lives in engine/crypto (where the wire
//     format is implemented), NOT here, to enforce the separation.
//
// Decoupling rule: a wire-envelope bump MAY ship without an
// application-protocol bump, and vice versa. They happen to advance
// together for v1.1 because per-client key derivation (wire change)
// and replay protection (wire change) both required new outer-
// envelope fields, and the negotiation handshake works better with
// matching protocol numbers.
const (
	ProtocolVersionMajor uint16 = 1
	ProtocolVersionMinor uint16 = 1
)

// IsSupportedVersion reports whether the runtime can interpret the
// given application-protocol version. v1.1 is a hard cut from v1.0:
// servers REJECT v1.0 envelopes, and clients MUST advertise 1.1 in
// PROBE.
func IsSupportedVersion(major, minor uint16) bool {
	return major == ProtocolVersionMajor && minor == ProtocolVersionMinor
}
