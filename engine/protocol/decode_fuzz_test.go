package protocol

import (
	"encoding/json"
	"testing"
)

// FuzzDecodeEnvelope explores the JSON envelope parser for panic
// inputs. Plan D2: this is the highest-leverage attack surface —
// any byte that gets past AEAD verification hits this code path.
//
// Seed corpus: representative valid envelopes covering each message
// type. The fuzzer mutates these into invalid shapes; the contract
// is "no panic, only structured errors".
func FuzzDecodeEnvelope(f *testing.F) {
	f.Add([]byte(`{"version":{"major":1,"minor":1},"client_id":"c","compression":"none","messages":[{"type":"PING","session_id":"s"}]}`))
	f.Add([]byte(`{"version":{"major":1,"minor":1},"client_id":"c","compression":"none","messages":[{"type":"OPEN","session_id":"s","target":{"network":"tcp","host":"example.com","port":443}}]}`))
	f.Add([]byte(`{"version":{"major":1,"minor":1},"client_id":"c","compression":"none","messages":[{"type":"DATA","session_id":"s","seq":0,"data":"AQID"}]}`))
	f.Add([]byte(`{"version":{"major":1,"minor":1},"client_id":"c","compression":"none","messages":[{"type":"PROBE","probe_id":"p"}]}`))
	f.Add([]byte(`{"version":{"major":1,"minor":1},"client_id":"c","compression":"none","messages":[{"type":"RESET","session_id":"s","code":"PEER_ERROR"}]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(``))

	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = DecodeEnvelope(raw)
		// The contract is "no panic". The error path is fully tested
		// in encode_test.go; here we just want to surface any
		// unexpected crash.
	})
}

// FuzzDecodeMessage isolates the per-message JSON unmarshaller from
// the envelope wrapper. Catches panics in the polymorphic message
// fields (Target, Seq, Data, etc.).
func FuzzDecodeMessage(f *testing.F) {
	f.Add([]byte(`{"type":"PING","session_id":"s"}`))
	f.Add([]byte(`{"type":"DATA","session_id":"s","seq":0,"data":"AA=="}`))
	f.Add([]byte(`{"type":"OPEN","session_id":"s","target":{"network":"tcp","host":"x","port":1}}`))
	f.Add([]byte(`{"type":"WAT"}`))
	f.Add([]byte(`{}`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		var m Message
		_ = json.Unmarshal(raw, &m)
	})
}
