package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const CompressionNone = "none"

type Version struct {
	Major uint16 `json:"major"`
	Minor uint16 `json:"minor"`
}

type Target struct {
	Network string `json:"network"`
	Host    string `json:"host"`
	Port    uint16 `json:"port"`
}

type Message struct {
	Type      MessageType `json:"-"`
	SessionID string      `json:"session_id,omitempty"`

	Target *Target `json:"target,omitempty"`

	Seq  *uint64 `json:"seq,omitempty"`
	Data []byte  `json:"data,omitempty"`
	// Compressed marks Data as gzip-compressed. When set, the receiver
	// MUST decompress before treating Data as application bytes.
	Compressed bool `json:"compressed,omitempty"`

	Code   string `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`

	Nonce string `json:"nonce,omitempty"`

	ProbeID           string    `json:"probe_id,omitempty"`
	Status            string    `json:"status,omitempty"`
	SupportedVersions []Version `json:"supported_versions,omitempty"`
	SelectedVersion   *Version  `json:"selected_version,omitempty"`
}

type Envelope struct {
	Version     Version   `json:"version"`
	ClientID    string    `json:"client_id"`
	Transport   string    `json:"transport,omitempty"`
	Compression string    `json:"compression"`
	Messages    []Message `json:"messages"`
}

type messageWire struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id,omitempty"`

	Target *Target `json:"target,omitempty"`

	Seq        *uint64 `json:"seq,omitempty"`
	Data       []byte  `json:"data,omitempty"`
	Compressed bool    `json:"compressed,omitempty"`

	Code   string `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`

	Nonce string `json:"nonce,omitempty"`

	ProbeID           string    `json:"probe_id,omitempty"`
	Status            string    `json:"status,omitempty"`
	SupportedVersions []Version `json:"supported_versions,omitempty"`
	SelectedVersion   *Version  `json:"selected_version,omitempty"`
}

func (m Message) MarshalJSON() ([]byte, error) {
	w := messageWire{
		Type:              m.Type.String(),
		SessionID:         m.SessionID,
		Target:            m.Target,
		Seq:               m.Seq,
		Data:              m.Data,
		Compressed:        m.Compressed,
		Code:              m.Code,
		Reason:            m.Reason,
		Nonce:             m.Nonce,
		ProbeID:           m.ProbeID,
		Status:            m.Status,
		SupportedVersions: m.SupportedVersions,
		SelectedVersion:   m.SelectedVersion,
	}
	if w.Type == "" {
		return nil, fmt.Errorf("protocol: cannot encode message with invalid type %d", uint8(m.Type))
	}
	return json.Marshal(w)
}

func (m *Message) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var w messageWire
	if err := dec.Decode(&w); err != nil {
		return err
	}
	mt, err := parseMessageType(w.Type)
	if err != nil {
		return err
	}
	*m = Message{
		Type:              mt,
		SessionID:         w.SessionID,
		Target:            w.Target,
		Seq:               w.Seq,
		Data:              w.Data,
		Compressed:        w.Compressed,
		Code:              w.Code,
		Reason:            w.Reason,
		Nonce:             w.Nonce,
		ProbeID:           w.ProbeID,
		Status:            w.Status,
		SupportedVersions: w.SupportedVersions,
		SelectedVersion:   w.SelectedVersion,
	}
	return nil
}

func EncodeEnvelope(env Envelope) ([]byte, error) {
	if err := validateEnvelope(&env); err != nil {
		return nil, err
	}
	return json.Marshal(env)
}
