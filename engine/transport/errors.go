package transport

import "errors"

var (
	// ErrUnreachable means the transport could not reach the remote at all.
	ErrUnreachable = errors.New("transport: remote unreachable")
	// ErrUpstreamRejected means the remote answered but rejected the batch.
	ErrUpstreamRejected = errors.New("transport: upstream rejected batch")
	// ErrInvalidResponse means the remote answered with a malformed reply.
	ErrInvalidResponse = errors.New("transport: invalid response")
	// ErrClosed is returned by transports after Close has been called.
	ErrClosed = errors.New("transport: closed")
)
