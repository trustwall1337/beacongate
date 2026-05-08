package protocol

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestCompressRoundTrip(t *testing.T) {
	in := []byte(strings.Repeat("the quick brown fox jumps over the lazy dog. ", 50))
	out, err := CompressData(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) >= len(in) {
		t.Fatalf("expected compression to help: in=%d out=%d", len(in), len(out))
	}
	got, err := DecompressData(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, in) {
		t.Fatalf("round-trip mismatch")
	}
}

func TestDecompressRejectsGarbage(t *testing.T) {
	_, err := DecompressData([]byte("not gzip"))
	if err == nil || !errors.Is(err, ErrDecompress) {
		t.Fatalf("expected decompress error, got %v", err)
	}
}

func TestDecompressBombGuard(t *testing.T) {
	// A 2 MiB input of zeros gzips to ~2 KiB. The output is well under the
	// MaxDecompressedSize cap; we just verify the guard fires when the
	// input claims to expand to something huge.
	bomb, err := CompressData(bytes.Repeat([]byte{0}, MaxDecompressedSize+1))
	if err != nil {
		t.Skipf("could not build bomb: %v", err)
	}
	if _, err := DecompressData(bomb); err == nil {
		t.Fatalf("expected decompress to refuse output > limit")
	}
}
