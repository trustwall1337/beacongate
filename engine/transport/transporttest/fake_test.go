package transporttest

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/trustwall1337/beacongate/engine/transport"
)

func TestFakeRoundtrip(t *testing.T) {
	f := &Fake{
		Handler: func(_ context.Context, b []byte) ([]byte, error) {
			return append([]byte("echo:"), b...), nil
		},
	}
	got, err := f.Roundtrip(context.Background(), []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("echo:hello")) {
		t.Fatalf("unexpected reply: %q", got)
	}
	d, err := f.Diagnose(context.Background())
	if err != nil || !d.Healthy {
		t.Fatalf("diag: %+v err=%v", d, err)
	}
}

func TestFakeClosed(t *testing.T) {
	f := &Fake{Handler: func(context.Context, []byte) ([]byte, error) { return nil, nil }}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Roundtrip(context.Background(), nil); !errors.Is(err, transport.ErrClosed) {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
	if _, err := f.Diagnose(context.Background()); !errors.Is(err, transport.ErrClosed) {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
}

func TestFakeNoHandler(t *testing.T) {
	f := &Fake{}
	if _, err := f.Roundtrip(context.Background(), nil); !errors.Is(err, transport.ErrInvalidResponse) {
		t.Fatalf("expected ErrInvalidResponse, got %v", err)
	}
}
