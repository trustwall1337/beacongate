// it is the BeaconGate AEAD-envelope wrapper imported as
// engine/crypto. Renaming would touch every consumer.
//
//nolint:revive // var-naming: package shadows stdlib "crypto" by design;
package crypto

import "testing"

// FuzzOpen explores the v1.1 wire-envelope parser for panic inputs.
// Plan D2: this is critical because the envelope parser runs BEFORE
// any AEAD check (the cleartext header — wire-version + client_id —
// must be parsed to know which key to derive). A panic here is a
// pre-auth crash on every tunnel request.
//
// Seed corpus: legitimate wire bytes plus pathological cases.
func FuzzOpen(f *testing.F) {
	// Build one valid wire envelope to seed with.
	key, err := GenerateKey()
	if err != nil {
		f.Fatal(err)
	}
	s, err := NewSealer(key)
	if err != nil {
		f.Fatal(err)
	}
	wire, err := s.Seal("client-fuzz", []byte(`{"hello":"world"}`))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(wire)

	// Pathological seeds.
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Add([]byte{0x01})                                                                   // version only
	f.Add([]byte{0xFF, 0xFF, 0xFF})                                                       // unknown version + huge id len
	f.Add([]byte{0x01, 0x00, 0x00})                                                       // valid version, zero id len
	f.Add([]byte{0x01, 0xFF, 0xFF})                                                       // valid version, max id len
	f.Add(append([]byte{0x01, 0x00, 0x05, 'a', 'b', 'c', 'd', 'e'}, make([]byte, 28)...)) // truncated AEAD

	f.Fuzz(func(t *testing.T, w []byte) {
		_, _ = s.Open(w)
	})
}
