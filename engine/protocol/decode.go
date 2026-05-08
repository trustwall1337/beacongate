package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

var (
	ErrMalformedEnvelope    = errors.New("protocol: malformed envelope")
	ErrUnsupportedVersion   = errors.New("protocol: unsupported version")
	ErrUnsupportedTransport = errors.New("protocol: unsupported compression")
	ErrInvalidMessage       = errors.New("protocol: invalid message")
)

func DecodeEnvelope(raw []byte) (Envelope, error) {
	var env Envelope
	if len(raw) == 0 {
		return env, fmt.Errorf("%w: empty payload", ErrMalformedEnvelope)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		return Envelope{}, fmt.Errorf("%w: %v", ErrMalformedEnvelope, err)
	}
	if dec.More() {
		return Envelope{}, fmt.Errorf("%w: trailing data after envelope", ErrMalformedEnvelope)
	}
	if err := validateEnvelope(&env); err != nil {
		return Envelope{}, err
	}
	return env, nil
}

func validateEnvelope(env *Envelope) error {
	if !IsSupportedVersion(env.Version.Major, env.Version.Minor) {
		return fmt.Errorf("%w: %d.%d", ErrUnsupportedVersion, env.Version.Major, env.Version.Minor)
	}
	if env.ClientID == "" {
		return fmt.Errorf("%w: client_id is required", ErrMalformedEnvelope)
	}
	if env.Compression != CompressionNone {
		return fmt.Errorf("%w: compression %q", ErrUnsupportedTransport, env.Compression)
	}
	if len(env.Messages) == 0 {
		return fmt.Errorf("%w: messages must not be empty", ErrMalformedEnvelope)
	}
	for i := range env.Messages {
		if err := validateMessage(&env.Messages[i]); err != nil {
			return fmt.Errorf("%w: message[%d]: %v", ErrInvalidMessage, i, err)
		}
	}
	return nil
}

func validateMessage(m *Message) error {
	if !IsValidMessageType(m.Type) {
		return fmt.Errorf("invalid type %d", uint8(m.Type))
	}
	switch m.Type {
	case MessageTypeOpen:
		if m.SessionID == "" {
			return errors.New("OPEN requires session_id")
		}
		if m.Target == nil {
			return errors.New("OPEN requires target")
		}
		if m.Target.Network != "tcp" {
			return fmt.Errorf("OPEN target.network must be tcp, got %q", m.Target.Network)
		}
		if m.Target.Host == "" {
			return errors.New("OPEN target.host required")
		}
		if m.Target.Port == 0 {
			return errors.New("OPEN target.port required")
		}
	case MessageTypeData:
		if m.SessionID == "" {
			return errors.New("DATA requires session_id")
		}
		if m.Seq == nil {
			return errors.New("DATA requires seq")
		}
	case MessageTypeClose:
		if m.SessionID == "" {
			return errors.New("CLOSE requires session_id")
		}
	case MessageTypeReset:
		if m.SessionID == "" {
			return errors.New("RESET requires session_id")
		}
		if m.Code == "" {
			return errors.New("RESET requires code")
		}
	case MessageTypePing:
		if m.SessionID == "" {
			return errors.New("PING requires session_id")
		}
	case MessageTypeProbe:
		if m.SessionID != "" {
			return errors.New("PROBE must not include session_id")
		}
		if m.ProbeID == "" {
			return errors.New("PROBE requires probe_id")
		}
	}
	return nil
}
