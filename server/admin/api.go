package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/trustwall1337/beacongate/server/policy"
	"github.com/trustwall1337/beacongate/server/runtime"
)

const (
	defaultAuthFailureThreshold = 8
	defaultAuthFailureWindow    = 5 * time.Minute
)

type API struct {
	auth    AuthConfig
	store   policy.Store
	engine  *policy.Engine
	server  *runtime.Server
	limiter *failureLimiter
}

func New(auth AuthConfig, store policy.Store, engine *policy.Engine, server *runtime.Server) *API {
	return &API{
		auth:    auth,
		store:   store,
		engine:  engine,
		server:  server,
		limiter: newFailureLimiter(defaultAuthFailureThreshold, defaultAuthFailureWindow),
	}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/policy/rules", a.handleRules)
	mux.HandleFunc("/api/policy/rules/", a.handleRule)
	mux.HandleFunc("/api/status", a.handleStatus)
	return a.middleware(mux)
}

func (a *API) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := remoteIP(r)
		now := time.Now()
		if ok, retry := a.limiter.allowed(ip, now); !ok {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", int(retry.Seconds())+1))
			a.writeError(w, &httpError{status: http.StatusTooManyRequests, msg: "too many failed attempts"})
			return
		}
		if err := a.auth.Authorize(r); err != nil {
			a.limiter.recordFailure(ip, now)
			a.writeError(w, err)
			return
		}
		a.limiter.recordSuccess(ip)
		next.ServeHTTP(w, r)
	})
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (a *API) writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	var herr *httpError
	if errors.As(err, &herr) {
		status = herr.Status()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func (a *API) writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (a *API) reload() {
	if a.engine == nil {
		return
	}
	a.engine.Replace(a.store.List())
}
