package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trustwall1337/beacongate/client/runtime"
	"github.com/trustwall1337/beacongate/engine/config"
	"github.com/trustwall1337/beacongate/engine/crypto"
	"github.com/trustwall1337/beacongate/engine/protocol"
	"github.com/trustwall1337/beacongate/engine/transport/transporttest"
)

func makeRuntime(t *testing.T) *runtime.Runtime {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.ClientConfig{
		ClientID:   "client-control",
		ListenAddr: "127.0.0.1:0",
		Server:     config.ClientServerConfig{URL: "http://x", Key: config.EncodeKey(key)},
		Transport:  config.ClientTransportConfig{Type: "fake"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	sealer, _ := crypto.NewSealer(key)
	ft := &transporttest.Fake{Handler: func(_ context.Context, ct []byte) ([]byte, error) {
		batch, _ := sealer.Open(ct)
		env, _ := protocol.DecodeEnvelope(batch.Plaintext)
		out := protocol.Envelope{
			Version: protocol.Version{Major: 1, Minor: 1}, ClientID: "fake",
			Compression: protocol.CompressionNone,
			Messages: []protocol.Message{{
				Type: protocol.MessageTypeProbe, ProbeID: env.Messages[0].ProbeID,
				Status:            "ok",
				SupportedVersions: []protocol.Version{{Major: 1, Minor: 1}},
				SelectedVersion:   &protocol.Version{Major: 1, Minor: 1},
			}},
		}
		raw, _ := protocol.EncodeEnvelope(out)
		return sealer.Seal(out.ClientID, raw)
	}}
	rt, err := runtime.New(cfg, ft)
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

func TestStatusEndpoint(t *testing.T) {
	rt := makeRuntime(t)
	defer rt.Close()
	api := New(rt)
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var sr StatusReport
	if err := json.Unmarshal(rec.Body.Bytes(), &sr); err != nil {
		t.Fatal(err)
	}
	if sr.ClientID != "client-control" {
		t.Fatalf("client id mismatch: %s", sr.ClientID)
	}
}

func TestNonLoopbackForbidden(t *testing.T) {
	rt := makeRuntime(t)
	defer rt.Close()
	api := New(rt)
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.RemoteAddr = "203.0.113.5:1234"
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestDiagnoseEndpoint(t *testing.T) {
	rt := makeRuntime(t)
	defer rt.Close()
	api := New(rt)
	req := httptest.NewRequest(http.MethodGet, "/api/diagnose", nil)
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if rec.Body.Len() == 0 || rec.Body.Bytes()[0] != '{' {
		t.Fatalf("expected JSON body, got %q", rec.Body.String())
	}
}

func TestStatusEndpointFullSchema(t *testing.T) {
	rt := makeRuntime(t)
	defer rt.Close()
	rt.SetActiveProfile("client_config.json")
	api := New(rt)

	// Drive a successful diagnostics run so state moves to "connected"
	// and LastSuccessfulProbe is populated.
	rt.RunStartupDiagnostics(context.Background())

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var sr StatusReport
	if err := json.Unmarshal(rec.Body.Bytes(), &sr); err != nil {
		t.Fatal(err)
	}
	if sr.State != "connected" {
		t.Errorf("state: want connected, got %q", sr.State)
	}
	if sr.ActiveProfile != "client_config.json" {
		t.Errorf("active_profile: want client_config.json, got %q", sr.ActiveProfile)
	}
	if sr.ListenAddr != "127.0.0.1:0" {
		t.Errorf("listen_addr: want 127.0.0.1:0, got %q", sr.ListenAddr)
	}
	if sr.TransportType != "fake" {
		t.Errorf("transport_type: want fake, got %q", sr.TransportType)
	}
	if !sr.TransportHealthy {
		t.Errorf("transport_healthy: want true")
	}
	if !sr.ProbeOK {
		t.Errorf("probe_ok: want true")
	}
	if sr.LastSuccessfulProbe.IsZero() {
		t.Errorf("last_successful_probe: want non-zero")
	}
	if sr.LastError != "" {
		t.Errorf("last_error: want empty, got %q", sr.LastError)
	}
}

func TestEventsEndpoint(t *testing.T) {
	rt := makeRuntime(t)
	defer rt.Close()
	api := New(rt)
	api.Events().Record(Event{Component: "runtime", Type: "connected", Summary: "tunnel up"})
	api.Events().Record(Event{Component: "transport", Level: "warn", Type: "probe_failed", Summary: "x"})

	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var events []Event
	if err := json.Unmarshal(rec.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("want 2 events, got %d", len(events))
	}
	if events[0].Type != "connected" || events[1].Type != "probe_failed" {
		t.Errorf("event order wrong: %v", events)
	}
}

func TestEventSinkRingEviction(t *testing.T) {
	s := NewEventSink(3)
	for i := 0; i < 5; i++ {
		s.Record(Event{Type: "x", Summary: string(rune('a' + i))})
	}
	got := s.Snapshot()
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
	// Oldest two ('a','b') should have been evicted; chronological order kept.
	if got[0].Summary != "c" || got[2].Summary != "e" {
		t.Errorf("unexpected eviction order: %+v", got)
	}
}

func TestValidateEndpoint(t *testing.T) {
	rt := makeRuntime(t)
	defer rt.Close()
	api := New(rt)
	req := httptest.NewRequest(http.MethodPost, "/api/validate", nil)
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var res ValidateResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if !res.OK || !res.ProbeOK {
		t.Fatalf("expected ok+probe_ok, got %+v", res)
	}
	if res.ConfigErr != "" {
		t.Errorf("unexpected config_err: %s", res.ConfigErr)
	}
}

func TestValidateEndpointRejectsGet(t *testing.T) {
	rt := makeRuntime(t)
	defer rt.Close()
	api := New(rt)
	req := httptest.NewRequest(http.MethodGet, "/api/validate", nil)
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}
