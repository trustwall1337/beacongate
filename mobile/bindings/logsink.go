package bindings

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
)

// LogSink is the interface the platform side (Android Kotlin / iOS
// Swift) implements to receive log events from the Go runtime. Each
// method receives a single human-readable line; the implementation
// forwards to whatever the platform's native logger is (Android
// Log.d/i/w/e, iOS os_log, etc.).
//
// gomobile generates a Java/Kotlin interface from this declaration —
// only methods using bindable types are allowed. The four severity
// methods + a single `string` argument are all gomobile-safe.
//
// **Concurrency:** the runtime calls these from arbitrary goroutines
// (the Pump's worker, the SOCKS handler, the transport probe loop).
// Implementations MUST be safe for concurrent calls.
type LogSink interface {
	Debug(message string)
	Info(message string)
	Warn(message string)
	Error(message string)
}

// SetLogSink installs a platform-side LogSink. Pass nil to silence.
// Replaces any previously installed sink.
//
// Wiring: this package builds a *slog.Logger backed by sinkHandler
// and installs it on the active runtime via Runtime.SetLogger() at
// StartTunnel time. If StartTunnel has not been called yet, the sink
// is held and applied to the next tunnel's runtime.
func SetLogSink(sink LogSink) {
	currentSink.Store(&sinkRef{sink: sink})
	// If a tunnel is already running, swap the logger immediately.
	if rt := currentRuntime(); rt != nil {
		rt.SetLogger(currentLogger())
	}
}

// currentSink is the atomically-swappable holder for the platform
// LogSink. Wrapping in a *sinkRef lets us atomic-store nil values
// safely (atomic.Pointer requires a typed nil).
var currentSink atomic.Pointer[sinkRef]

// sinkRef wraps a LogSink so currentSink can hold (effectively) nil
// without an interface-shaped type assertion at every load.
type sinkRef struct {
	sink LogSink // may be nil → silent
}

// currentLogger returns the *slog.Logger that the active runtime
// should use, reflecting the most recently installed LogSink. If no
// sink is set, returns a discard-handler-backed logger. Safe to call
// from any goroutine.
func currentLogger() *slog.Logger {
	ref := currentSink.Load()
	if ref == nil || ref.sink == nil {
		return slog.New(discardHandler{})
	}
	return slog.New(&sinkHandler{sink: ref.sink})
}

// discardHandler is a no-op slog.Handler used when no LogSink is set.
// Cheaper than slog.NewTextHandler(io.Discard, ...) which still
// formats records before throwing them away.
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (discardHandler) WithAttrs([]slog.Attr) slog.Handler        { return discardHandler{} }
func (discardHandler) WithGroup(string) slog.Handler             { return discardHandler{} }

// sinkHandler is a slog.Handler that formats each record as a single
// line and dispatches to the appropriate severity method on the
// LogSink. It bridges the Go runtime's structured logging onto
// Android logcat (or any other platform logger).
//
// Format: "<message> [k1=v1 k2=v2 ...]" — the level is conveyed by
// which method we call on the sink, so it's not duplicated in the
// line itself. Attributes from With/WithGroup chains are flattened
// into the kv list with dot-separated group prefixes
// ("group.key=value").
type sinkHandler struct {
	sink   LogSink
	groups []string
	attrs  []slog.Attr
	mu     sync.Mutex // guards line construction; cheap on Android (one log/sec)
}

func (h *sinkHandler) Enabled(_ context.Context, level slog.Level) bool {
	// Always enabled. The platform side filters by level if it
	// wants; sending all records lets a future "show debug logs"
	// toggle in the UI work without restarting the tunnel.
	return true
}

func (h *sinkHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	var b strings.Builder
	b.WriteString(r.Message)
	// Pre-installed attrs (from With chains).
	for _, a := range h.attrs {
		appendAttr(&b, h.groups, a)
	}
	// Per-record attrs.
	r.Attrs(func(a slog.Attr) bool {
		appendAttr(&b, h.groups, a)
		return true
	})
	line := b.String()

	switch {
	case r.Level >= slog.LevelError:
		h.sink.Error(line)
	case r.Level >= slog.LevelWarn:
		h.sink.Warn(line)
	case r.Level >= slog.LevelInfo:
		h.sink.Info(line)
	default:
		h.sink.Debug(line)
	}
	return nil
}

func (h *sinkHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	merged = append(merged, h.attrs...)
	merged = append(merged, attrs...)
	return &sinkHandler{sink: h.sink, groups: h.groups, attrs: merged}
}

func (h *sinkHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	g := make([]string, 0, len(h.groups)+1)
	g = append(g, h.groups...)
	g = append(g, name)
	return &sinkHandler{sink: h.sink, groups: g, attrs: h.attrs}
}

// appendAttr formats a single attr onto b in " key=value" form,
// applying any group prefixes. Recursive for nested groups.
func appendAttr(b *strings.Builder, groups []string, a slog.Attr) {
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		nested := make([]string, 0, len(groups)+1)
		nested = append(nested, groups...)
		if a.Key != "" {
			nested = append(nested, a.Key)
		}
		for _, sub := range a.Value.Group() {
			appendAttr(b, nested, sub)
		}
		return
	}
	b.WriteByte(' ')
	for _, g := range groups {
		b.WriteString(g)
		b.WriteByte('.')
	}
	b.WriteString(a.Key)
	b.WriteByte('=')
	fmt.Fprint(b, a.Value.Any())
}
