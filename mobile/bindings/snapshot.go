// Package bindings is the gomobile-bind facade for the BeaconGate Go
// client. It is consumed exclusively by the Android (and future iOS)
// app via `gomobile bind -target=android ./mobile/bindings`, which
// produces an .aar (or .xcframework) the platform code links against.
//
// gomobile imposes constraints on what crosses the JNI boundary:
//
//   - Exported function signatures may use only: int*, float*, bool,
//     string, []byte, error, package-defined struct pointers, and
//     simple interfaces.
//   - time.Time, time.Duration, generics, channels, map[string]any,
//     and func-typed fields are rejected.
//
// This package wraps the existing client/runtime + engine/* code in
// gomobile-clean shapes. The wrappers carry the rich Go types in
// unexported fields (which gomobile hides from the JNI surface) and
// expose only safe scalar fields + methods to the platform side.
//
// **One tunnel per process.** Android only ever needs a single tunnel
// active at a time; the package keeps the active runtime + pump +
// SOCKS server in mutex-protected globals. Calling StartTunnel while
// one is already running returns an error.
package bindings

import "github.com/trustwall1337/beacongate/engine/config"

// ConfigSnapshot is a gomobile-clean view of a parsed and validated
// engine/config.ClientConfig. The raw config is held in an unexported
// field (which gomobile hides from the JNI surface) so the platform
// side can pass the snapshot back to StartTunnel without having to
// reconstruct the config from scalar fields.
//
// Exported fields are populated for display in the Android UI's
// "imported config summary" line.
type ConfigSnapshot struct {
	// raw is the validated config. Unexported so gomobile does not
	// try to expose ClientConfig's map[string]any Options field
	// across JNI (which would fail at bind time).
	raw *config.ClientConfig

	ClientID      string
	ListenAddr    string
	TransportType string
}

// StatusSnapshot is the gomobile-clean view of the runtime's current
// state. All time fields are int64 Unix millis (gomobile rejects
// time.Time). State is the runtime.State string ("starting",
// "connected", "degraded", "error", "stopped").
type StatusSnapshot struct {
	// State is the lifecycle state. "stopped" when no tunnel is
	// running.
	State string
	// ClientID is the active config's client_id, or "" when stopped.
	ClientID string
	// ListenAddr is "host:port" of the local SOCKS5 listener, or
	// "" when stopped.
	ListenAddr string
	// TransportType is "appsscript" / "https" / "" when stopped.
	TransportType string
	// TransportHealthy is the result of the last transport
	// Diagnose() call. False on a stopped tunnel.
	TransportHealthy bool
	// LastSuccessfulProbeMs is the Unix-millis timestamp of the
	// last successful end-to-end probe. 0 when no probe has
	// succeeded yet (or the tunnel is stopped).
	LastSuccessfulProbeMs int64
	// LastError is a human-readable string describing the most
	// recent transport-side failure, or "" if healthy.
	LastError string
}

// Stats is a gomobile-clean view of the per-tunnel byte and session
// counters. v1 returns zeros for the byte counters (counter
// integration into Pump deferred to a follow-up patch); the
// SessionCount field is populated when the runtime exposes it.
//
// Kept as a separate struct so future fields can be added without
// breaking the StatusSnapshot wire shape.
type Stats struct {
	// BytesIn is the total bytes received from upstream over this
	// tunnel's lifetime. Returns 0 on a stopped tunnel.
	//
	// **v1 stub: always 0.** Real counters require Pump
	// instrumentation; deferred to v2 once the UI for byte
	// display lands.
	BytesIn int64
	// BytesOut mirrors BytesIn for the upstream-bound direction.
	BytesOut int64
	// SessionCount is the number of live SOCKS sessions currently
	// being multiplexed through the tunnel. 0 when stopped.
	//
	// **v1 stub: always 0.** Same rationale as BytesIn.
	SessionCount int32
}
