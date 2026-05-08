package protocol

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
)

// CompressThreshold is the minimum payload size at which gzip compression
// pays off. Below this, the gzip header (~20 bytes) costs more than it
// saves; small DATA messages and all session-control messages stay raw.
const CompressThreshold = 256

// MaxDecompressedSize bounds gzip output to defend against decompression
// bombs. 16 MiB is well above any sensible single-chunk size.
const MaxDecompressedSize = 16 * 1024 * 1024

var ErrDecompress = errors.New("protocol: decompress failed")

// CompressData gzip-compresses b. Callers should only invoke this when
// len(b) >= CompressThreshold; below that the result will be larger than
// the input.
func CompressData(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DecompressData inverts CompressData. It caps the output at
// MaxDecompressedSize to guard against compression bombs from a malicious
// peer.
func DecompressData(b []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, errors.Join(ErrDecompress, err)
	}
	defer r.Close()
	out, err := io.ReadAll(io.LimitReader(r, MaxDecompressedSize+1))
	if err != nil {
		return nil, errors.Join(ErrDecompress, err)
	}
	if len(out) > MaxDecompressedSize {
		return nil, ErrDecompress
	}
	return out, nil
}
