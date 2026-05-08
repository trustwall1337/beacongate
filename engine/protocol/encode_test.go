package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func u64(v uint64) *uint64 { return &v }

func validOpenEnvelope() Envelope {
	return Envelope{
		Version:     Version{Major: ProtocolVersionMajor, Minor: ProtocolVersionMinor},
		ClientID:    "client-alpha",
		Transport:   "google",
		Compression: CompressionNone,
		Messages: []Message{
			{
				Type:      MessageTypeOpen,
				SessionID: "sess-001",
				Target:    &Target{Network: "tcp", Host: "example.com", Port: 443},
			},
			{
				Type:      MessageTypeData,
				SessionID: "sess-001",
				Seq:       u64(0),
				Data:      []byte{1, 2, 3, 4},
			},
		},
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	env := validOpenEnvelope()
	raw, err := EncodeEnvelope(env)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeEnvelope(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ClientID != env.ClientID {
		t.Fatalf("client_id mismatch: %s", got.ClientID)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got.Messages))
	}
	if got.Messages[0].Type != MessageTypeOpen {
		t.Fatalf("expected OPEN, got %v", got.Messages[0].Type)
	}
	if got.Messages[1].Type != MessageTypeData {
		t.Fatalf("expected DATA, got %v", got.Messages[1].Type)
	}
	if !bytes.Equal(got.Messages[1].Data, []byte{1, 2, 3, 4}) {
		t.Fatalf("data mismatch: %v", got.Messages[1].Data)
	}
	if got.Messages[1].Seq == nil || *got.Messages[1].Seq != 0 {
		t.Fatalf("seq mismatch")
	}
}

func TestEncodeDecodeReset(t *testing.T) {
	env := Envelope{
		Version:     Version{Major: 1, Minor: 0},
		ClientID:    "server-west-1",
		Compression: CompressionNone,
		Messages: []Message{
			{Type: MessageTypeReset, SessionID: "sess-001", Code: "POLICY_DENIED", Reason: "blocked"},
		},
	}
	raw, err := EncodeEnvelope(env)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeEnvelope(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Messages[0].Code != "POLICY_DENIED" {
		t.Fatalf("code mismatch")
	}
}

func TestEncodeDecodeProbe(t *testing.T) {
	env := Envelope{
		Version:     Version{Major: 1, Minor: 0},
		ClientID:    "server-west-1",
		Compression: CompressionNone,
		Messages: []Message{
			{
				Type:              MessageTypeProbe,
				ProbeID:           "probe-42",
				Status:            "ok",
				SupportedVersions: []Version{{Major: 1, Minor: 0}},
				SelectedVersion:   &Version{Major: 1, Minor: 0},
			},
		},
	}
	raw, err := EncodeEnvelope(env)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeEnvelope(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Messages[0].Status != "ok" {
		t.Fatalf("status mismatch")
	}
	if got.Messages[0].SelectedVersion == nil {
		t.Fatalf("selected_version missing")
	}
}

func TestDecodeRejectsEmpty(t *testing.T) {
	if _, err := DecodeEnvelope(nil); !errors.Is(err, ErrMalformedEnvelope) {
		t.Fatalf("expected malformed error for empty input, got %v", err)
	}
}

func TestDecodeRejectsTruncated(t *testing.T) {
	if _, err := DecodeEnvelope([]byte("{\"version\"")); !errors.Is(err, ErrMalformedEnvelope) {
		t.Fatalf("expected malformed error, got %v", err)
	}
}

func TestDecodeRejectsUnsupportedVersion(t *testing.T) {
	env := validOpenEnvelope()
	env.Version.Minor = 1
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeEnvelope(raw); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("expected unsupported version, got %v", err)
	}
}

func TestDecodeRejectsBadCompression(t *testing.T) {
	env := validOpenEnvelope()
	env.Compression = "gzip"
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeEnvelope(raw); !errors.Is(err, ErrUnsupportedTransport) {
		t.Fatalf("expected unsupported compression, got %v", err)
	}
}

func TestDecodeRejectsMissingClientID(t *testing.T) {
	env := validOpenEnvelope()
	env.ClientID = ""
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeEnvelope(raw); !errors.Is(err, ErrMalformedEnvelope) {
		t.Fatalf("expected malformed, got %v", err)
	}
}

func TestDecodeRejectsEmptyMessages(t *testing.T) {
	env := Envelope{
		Version:     Version{Major: 1, Minor: 0},
		ClientID:    "c",
		Compression: CompressionNone,
		Messages:    []Message{},
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeEnvelope(raw); !errors.Is(err, ErrMalformedEnvelope) {
		t.Fatalf("expected malformed, got %v", err)
	}
}

func TestDecodeRejectsUnknownMessageType(t *testing.T) {
	raw := []byte(`{"version":{"major":1,"minor":0},"client_id":"c","compression":"none","messages":[{"type":"WAT","session_id":"s"}]}`)
	if _, err := DecodeEnvelope(raw); !errors.Is(err, ErrMalformedEnvelope) {
		t.Fatalf("expected malformed, got %v", err)
	}
}

func TestDecodeRejectsUnknownTopLevelField(t *testing.T) {
	raw := []byte(`{"version":{"major":1,"minor":0},"client_id":"c","compression":"none","messages":[{"type":"PING","session_id":"s"}],"extra":"x"}`)
	if _, err := DecodeEnvelope(raw); !errors.Is(err, ErrMalformedEnvelope) {
		t.Fatalf("expected malformed for unknown field, got %v", err)
	}
}

func TestDecodeRejectsUnknownMessageField(t *testing.T) {
	raw := []byte(`{"version":{"major":1,"minor":0},"client_id":"c","compression":"none","messages":[{"type":"PING","session_id":"s","wat":1}]}`)
	if _, err := DecodeEnvelope(raw); !errors.Is(err, ErrMalformedEnvelope) {
		t.Fatalf("expected malformed for unknown message field, got %v", err)
	}
}

func TestDecodeRejectsOpenWithoutTarget(t *testing.T) {
	env := Envelope{
		Version:     Version{Major: 1, Minor: 0},
		ClientID:    "c",
		Compression: CompressionNone,
		Messages: []Message{
			{Type: MessageTypeOpen, SessionID: "s"},
		},
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeEnvelope(raw); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("expected invalid message, got %v", err)
	}
}

func TestDecodeRejectsDataWithoutSeq(t *testing.T) {
	raw := []byte(`{"version":{"major":1,"minor":0},"client_id":"c","compression":"none","messages":[{"type":"DATA","session_id":"s"}]}`)
	if _, err := DecodeEnvelope(raw); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("expected invalid message, got %v", err)
	}
}

func TestEncodeRejectsInvalidType(t *testing.T) {
	env := Envelope{
		Version:     Version{Major: 1, Minor: 0},
		ClientID:    "c",
		Compression: CompressionNone,
		Messages:    []Message{{Type: MessageType(99), SessionID: "s"}},
	}
	if _, err := EncodeEnvelope(env); err == nil {
		t.Fatalf("expected encode error for invalid type")
	}
}
