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
