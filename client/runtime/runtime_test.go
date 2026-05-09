package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/trustwall1337/beacongate/engine/config"
	"github.com/trustwall1337/beacongate/engine/crypto"
	"github.com/trustwall1337/beacongate/engine/protocol"
	"github.com/trustwall1337/beacongate/engine/transport/transporttest"
)

func makeRuntime(t *testing.T, handler func(env protocol.Envelope) protocol.Envelope) *Runtime {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := crypto.NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.ClientConfig{
		ClientID:   "client-test",
		ListenAddr: "127.0.0.1:0",
		Server:     config.ClientServerConfig{URL: "http://example", Key: config.EncodeKey(key)},
		Transport:  config.ClientTransportConfig{Type: "fake"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	ft := &transporttest.Fake{Handler: func(_ context.Context, ct []byte) ([]byte, error) {
		batch, err := sealer.Open(ct)
		if err != nil {
			return nil, err
		}
		env, err := protocol.DecodeEnvelope(batch.Plaintext)
		if err != nil {
			return nil, err
		}
		out := handler(env)
		raw, err := protocol.EncodeEnvelope(out)
		if err != nil {
			return nil, err
		}
		return sealer.Seal(out.ClientID, raw)
	}}
	rt, err := New(cfg, ft)
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

func TestExchangeRoundTrip(t *testing.T) {
	rt := makeRuntime(t, func(env protocol.Envelope) protocol.Envelope {
		// echo the OPEN with a RESET to keep things minimal
		return protocol.Envelope{
			Version:     protocol.Version{Major: 1, Minor: 1},
			ClientID:    "server",
			Compression: protocol.CompressionNone,
			Messages: []protocol.Message{
				{Type: protocol.MessageTypeReset, SessionID: env.Messages[0].SessionID, Code: protocol.MessageType(0).String(), Reason: "test"},
			},
		}
	})
	defer rt.Close()

	id, err := rt.NewID("sess")
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := rt.Exchange(context.Background(), []protocol.Message{
		{Type: protocol.MessageTypeOpen, SessionID: id, Target: &protocol.Target{Network: "tcp", Host: "x", Port: 1}},
	})
	// The fake server hands back RESET with empty code which is invalid; we
	// rely on the runtime's response validator surfacing that as an error.
	if err == nil {
		t.Fatalf("expected validation error from invalid RESET code, got msgs %v", msgs)
	}
}

func TestExchangeRejectsEmptyBatch(t *testing.T) {
	rt := makeRuntime(t, func(protocol.Envelope) protocol.Envelope { return protocol.Envelope{} })
	defer rt.Close()
	if _, err := rt.Exchange(context.Background(), nil); err == nil {
		t.Fatalf("expected error on empty batch")
	}
}

func TestProbeReceivesResponse(t *testing.T) {
	rt := makeRuntime(t, func(env protocol.Envelope) protocol.Envelope {
		probe := env.Messages[0]
		return protocol.Envelope{
			Version:     protocol.Version{Major: 1, Minor: 1},
			ClientID:    "server",
			Compression: protocol.CompressionNone,
			Messages: []protocol.Message{
				{
					Type:              protocol.MessageTypeProbe,
					ProbeID:           probe.ProbeID,
					Status:            "ok",
					SupportedVersions: []protocol.Version{{Major: 1, Minor: 1}},
					SelectedVersion:   &protocol.Version{Major: 1, Minor: 1},
				},
			},
		}
	})
	defer rt.Close()
	resp, err := rt.Probe(context.Background())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("status: %s", resp.Status)
	}
}

func TestExchangeAfterCloseFails(t *testing.T) {
	rt := makeRuntime(t, func(protocol.Envelope) protocol.Envelope { return protocol.Envelope{} })
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Exchange(context.Background(), []protocol.Message{{Type: protocol.MessageTypePing, SessionID: "x"}}); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
}
