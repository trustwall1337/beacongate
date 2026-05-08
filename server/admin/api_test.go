package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/trustwall1337/beacongate/server/policy"
)

func newTestAPI(t *testing.T, mode AuthMode, token string) (*API, policy.Store, *policy.Engine, func()) {
	t.Helper()
	store, err := policy.OpenFileStore(filepath.Join(t.TempDir(), "p.json"))
	if err != nil {
		t.Fatal(err)
	}
	engine := policy.NewEngine()
	api := New(AuthConfig{Mode: mode, Token: token}, store, engine, nil)
	return api, store, engine, func() {}
}

func TestLocalOnlyRejectsRemote(t *testing.T) {
	api, _, _, cleanup := newTestAPI(t, AuthLocalOnly, "")
	defer cleanup()
	req := httptest.NewRequest(http.MethodGet, "/api/policy/rules", nil)
	req.RemoteAddr = "203.0.113.5:1234"
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestLocalOnlyAllowsLoopback(t *testing.T) {
	api, _, _, cleanup := newTestAPI(t, AuthLocalOnly, "")
	defer cleanup()
	req := httptest.NewRequest(http.MethodGet, "/api/policy/rules", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestBearerTokenAuth(t *testing.T) {
	api, _, _, cleanup := newTestAPI(t, AuthBearerToken, "secret-token")
	defer cleanup()
	// missing token
	req := httptest.NewRequest(http.MethodGet, "/api/policy/rules", nil)
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	// wrong token
	req.Header.Set("Authorization", "Bearer wrong")
	rec = httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	// right token
	req.Header.Set("Authorization", "Bearer secret-token")
	rec = httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestRulesCRUDFlow(t *testing.T) {
	api, _, engine, cleanup := newTestAPI(t, AuthLocalOnly, "")
	defer cleanup()
	srv := httptest.NewServer(api.Handler())
	defer srv.Close()
	url := srv.URL + "/api/policy/rules"

	body := []byte(`{"id":"r1","action":"block","match":"exact-host","pattern":"x","enabled":true}`)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	resp.Body.Close()

	// engine should have been reloaded
	d := engine.Evaluate("x", 0)
	if d.Allowed {
		t.Fatalf("expected blocked after upsert")
	}

	// list
	resp, err = http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	var list []map[string]any
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(list))
	}

	// delete
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/policy/rules/r1", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status %d", resp.StatusCode)
	}
	resp.Body.Close()

	if d := engine.Evaluate("x", 0); !d.Allowed {
		t.Fatalf("expected allowed after delete")
	}
}

func TestInvalidRuleRejected(t *testing.T) {
	api, _, _, cleanup := newTestAPI(t, AuthLocalOnly, "")
	defer cleanup()
	srv := httptest.NewServer(api.Handler())
	defer srv.Close()

	body := []byte(`{"id":"","action":"block","match":"exact-host","pattern":"x"}`)
	resp, err := http.Post(srv.URL+"/api/policy/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}
