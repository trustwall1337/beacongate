package https

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trustwall1337/beacongate/engine/transport"
)

func TestRoundtripEcho(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", contentTypeOpaque)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(append([]byte("echo:"), body...))
	}))
	defer srv.Close()
	c, err := New(Config{URL: srv.URL, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Roundtrip(context.Background(), []byte("payload"))
	if err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	if !bytes.Equal(got, []byte("echo:payload")) {
		t.Fatalf("unexpected body: %q", got)
	}
}

func TestRoundtripSendsContentType(t *testing.T) {
	var gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	c, _ := New(Config{URL: srv.URL, HTTPClient: srv.Client()})
	if _, err := c.Roundtrip(context.Background(), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if gotCT != contentTypeOpaque {
		t.Fatalf("content-type %q", gotCT)
	}
}

func TestRoundtripSendsFrontingHost(t *testing.T) {
	var gotHost string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	c, _ := New(Config{URL: srv.URL, HTTPClient: srv.Client(), FrontingHost: "fronted.example.com"})
	if _, err := c.Roundtrip(context.Background(), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if gotHost != "fronted.example.com" {
		t.Fatalf("Host header = %q", gotHost)
	}
}

func TestRoundtripUpstreamRejectedOn4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	c, _ := New(Config{URL: srv.URL, HTTPClient: srv.Client()})
	_, err := c.Roundtrip(context.Background(), []byte("x"))
	if !errors.Is(err, transport.ErrUpstreamRejected) {
		t.Fatalf("want ErrUpstreamRejected, got %v", err)
	}
}

func TestRoundtripUnreachableOn5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	c, _ := New(Config{URL: srv.URL, HTTPClient: srv.Client()})
	_, err := c.Roundtrip(context.Background(), []byte("x"))
	if !errors.Is(err, transport.ErrUnreachable) {
		t.Fatalf("want ErrUnreachable, got %v", err)
	}
}

func TestRoundtripInvalidWhenEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c, _ := New(Config{URL: srv.URL, HTTPClient: srv.Client()})
	_, err := c.Roundtrip(context.Background(), []byte("x"))
	if !errors.Is(err, transport.ErrInvalidResponse) {
		t.Fatalf("want ErrInvalidResponse, got %v", err)
	}
}

func TestDiagnoseHealthURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/healthz") {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c, _ := New(Config{URL: srv.URL + "/tunnel", HealthURL: srv.URL + "/healthz", HTTPClient: srv.Client()})
	diag, err := c.Diagnose(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !diag.Healthy {
		t.Fatalf("expected healthy: %+v", diag)
	}
}

func TestNewRequiresURL(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatalf("expected error for empty URL")
	}
}

func TestClosedTransport(t *testing.T) {
	c, _ := New(Config{URL: "http://example.com"})
	_ = c.Close()
	if _, err := c.Roundtrip(context.Background(), nil); !errors.Is(err, transport.ErrClosed) {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
	if _, err := c.Diagnose(context.Background()); !errors.Is(err, transport.ErrClosed) {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
}
