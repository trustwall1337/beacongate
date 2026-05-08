// Package admin exposes a small HTTP API for managing server policy and
// inspecting runtime status. It supports local-only mode (no auth, loopback
// listener) and remote mode (bearer token over a non-loopback listener).
package admin

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

type AuthMode int

const (
	AuthLocalOnly AuthMode = iota
	AuthBearerToken
)

type AuthConfig struct {
	Mode  AuthMode
	Token string
}

// Authorize returns nil when the request satisfies the configured mode.
func (a AuthConfig) Authorize(r *http.Request) error {
	switch a.Mode {
	case AuthLocalOnly:
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return errUnauthorized("local-only admin endpoint")
		}
		return nil
	case AuthBearerToken:
		header := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(header, prefix) {
			return errUnauthorized("missing bearer token")
		}
		got := strings.TrimSpace(header[len(prefix):])
		if a.Token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(a.Token)) != 1 {
			return errUnauthorized("invalid token")
		}
		return nil
	}
	return errUnauthorized("unknown auth mode")
}

type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }
func (e *httpError) Status() int   { return e.status }

func errUnauthorized(msg string) *httpError {
	return &httpError{status: http.StatusUnauthorized, msg: msg}
}
