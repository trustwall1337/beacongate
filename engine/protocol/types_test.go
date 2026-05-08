package protocol

import "testing"

func TestMessageTypeValidation(t *testing.T) {
	tests := []struct {
		name    string
		value   MessageType
		isValid bool
	}{
		{name: "open", value: MessageTypeOpen, isValid: true},
		{name: "data", value: MessageTypeData, isValid: true},
		{name: "close", value: MessageTypeClose, isValid: true},
		{name: "reset", value: MessageTypeReset, isValid: true},
		{name: "ping", value: MessageTypePing, isValid: true},
		{name: "probe", value: MessageTypeProbe, isValid: true},
		{name: "unknown", value: MessageType(99), isValid: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsValidMessageType(tc.value); got != tc.isValid {
				t.Fatalf("expected validity %t for %v, got %t", tc.isValid, tc.value, got)
			}
		})
	}
}

func TestProtocolVersionConstants(t *testing.T) {
	if ProtocolVersionMajor != 1 {
		t.Fatalf("expected major version 1, got %d", ProtocolVersionMajor)
	}

	if ProtocolVersionMinor != 0 {
		t.Fatalf("expected minor version 0, got %d", ProtocolVersionMinor)
	}
}

func TestProtocolVersionSupport(t *testing.T) {
	tests := []struct {
		name      string
		major     uint16
		minor     uint16
		supported bool
	}{
		{name: "current version", major: ProtocolVersionMajor, minor: ProtocolVersionMinor, supported: true},
		{name: "lower major", major: 0, minor: 9, supported: false},
		{name: "higher minor", major: 1, minor: 1, supported: false},
		{name: "higher major", major: 2, minor: 0, supported: false},
		{name: "zero version", major: 0, minor: 0, supported: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSupportedVersion(tc.major, tc.minor); got != tc.supported {
				t.Fatalf("expected support=%t for %d.%d, got %t", tc.supported, tc.major, tc.minor, got)
			}
		})
	}
}
