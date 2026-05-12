// Package main runs a local-only web playground for testing
// BeaconGate client configs interactively.
//
// Two modes:
//
//   - **Smoke test:** paste JSON → click "Run smoke test" → playground
//     runs preflight + one SOCKS5 GET to api.ipify.org through the
//     in-process tunnel, reports timing.
//   - **Browser proxy mode (the real one):** click "Connect" → playground
//     keeps a persistent in-process tunnel running on a local SOCKS5
//     port → you point your browser's SOCKS5 proxy at that port and
//     browse normally → DevTools Network tab shows accurate
//     end-user-perceived timing for full page loads including all
//     sub-resources. Disconnect to tear down.
//
// Run from the repo root:
//
//	go run ./test/playground
//
// Then open http://127.0.0.1:9080 in a browser. Loopback only.
// One active tunnel at a time (serialized) so two browser tabs cannot
// race the in-process transport state.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/proxy"

	clientruntime "github.com/trustwall1337/beacongate/client/runtime"
	"github.com/trustwall1337/beacongate/client/socks"
	"github.com/trustwall1337/beacongate/engine/config"
	"github.com/trustwall1337/beacongate/engine/transport"
	"github.com/trustwall1337/beacongate/engine/transport/appsscript"
	httpstransport "github.com/trustwall1337/beacongate/engine/transport/https"
)

//go:embed index.html
var staticFS embed.FS

const (
	listenAddr        = "127.0.0.1:9080"
	smokeURL          = "http://api.ipify.org/"
	preflightTimeout  = 20 * time.Second
	smokeRequestLimit = 20 * time.Second
	maxActivityLog    = 200
)

// tunnelSession holds an active in-process client. Serialized via
// stateMu so only one tunnel is ever running at a time.
type tunnelSession struct {
	cfg          *config.ClientConfig
	tr           transport.ClientTransport
	rt           *clientruntime.Runtime
	pump         *clientruntime.Pump
	socksSrv     *socks.Server
	listener     net.Listener
	socksAddr    string
	startedAt    time.Time
	connCount    atomic.Int64
	activityLog  *activityLog
	preflightOK  bool
	preflightMS  int64
	preflightMsg string
}

type stateMu struct {
	mu     sync.Mutex
	active *tunnelSession
}

var state = &stateMu{}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/api/connect", handleConnect)
	mux.HandleFunc("/api/disconnect", handleDisconnect)
	mux.HandleFunc("/api/status", handleStatus)
	mux.HandleFunc("/api/smoke", handleSmoke)
	mux.HandleFunc("/api/activity", handleActivity)
	mux.HandleFunc("/api/launch-browser", handleLaunchBrowser)
	log.Printf("BeaconGate playground listening on http://%s", listenAddr)
	log.Println("Open that URL in a browser. Ctrl-C to stop.")
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		log.Fatal(err)
	}
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	body, err := staticFS.ReadFile("index.html")
	if err != nil {
		http.Error(w, "missing index.html", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body)
}

// --- /api/connect ---

type connectRequest struct {
	Config string `json:"config"`
}

type connectResponse struct {
	OK          bool       `json:"ok"`
	Error       string     `json:"error,omitempty"`
	Steps       []testStep `json:"steps"`
	SocksAddr   string     `json:"socks_addr,omitempty"`
	ClientID    string     `json:"client_id,omitempty"`
	TransportT  string     `json:"transport_type,omitempty"`
	PreflightMS int64      `json:"preflight_ms,omitempty"`
}

type testStep struct {
	Name      string `json:"name"`
	OK        bool   `json:"ok"`
	Detail    string `json:"detail,omitempty"`
	ElapsedMS int64  `json:"elapsed_ms,omitempty"`
}

func handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.active != nil {
		writeJSON(w, http.StatusConflict, connectResponse{
			OK:    false,
			Error: "already connected — disconnect first",
		})
		return
	}

	var req connectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, connectResponse{
			OK:    false,
			Error: "bad request body: " + err.Error(),
		})
		return
	}
	sess, steps, summaryErr := connectTunnel(req.Config)
	resp := connectResponse{Steps: steps}
	if sess == nil {
		resp.OK = false
		resp.Error = summaryErr
		writeJSON(w, http.StatusOK, resp)
		return
	}
	state.active = sess
	resp.OK = true
	resp.SocksAddr = sess.socksAddr
	resp.ClientID = sess.cfg.ClientID
	resp.TransportT = sess.cfg.Transport.Type
	resp.PreflightMS = sess.preflightMS
	writeJSON(w, http.StatusOK, resp)
	log.Printf("connect ok: client_id=%s socks=%s preflight=%dms",
		sess.cfg.ClientID, sess.socksAddr, sess.preflightMS)
}

// connectTunnel does the full setup pipeline. Returns (session, steps,
// summary-error-string). On failure, session is nil and summaryErr is
// populated. On success, session is non-nil and tracked-in-state by
// the caller (handleConnect).
func connectTunnel(configJSON string) (*tunnelSession, []testStep, string) {
	var steps []testStep

	// step 1: JSON syntax
	t1 := time.Now()
	var probe map[string]any
	if err := json.Unmarshal([]byte(configJSON), &probe); err != nil {
		steps = append(steps, testStep{
			Name: "Validate JSON syntax", OK: false,
			Detail:    err.Error(),
			ElapsedMS: time.Since(t1).Milliseconds(),
		})
		return nil, steps, "JSON syntax error"
	}
	steps = append(steps, testStep{
		Name: "Validate JSON syntax", OK: true,
		ElapsedMS: time.Since(t1).Milliseconds(),
	})

	// step 2: schema validation via beacongate's own loader
	t2 := time.Now()
	tmp, err := os.CreateTemp("", "bg-playground-*.json")
	if err != nil {
		return nil, steps, "create tempfile: " + err.Error()
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	_ = tmp.Chmod(0o600)
	if _, err := tmp.WriteString(configJSON); err != nil {
		_ = tmp.Close()
		return nil, steps, "write tempfile: " + err.Error()
	}
	_ = tmp.Close()

	cfg, err := config.LoadClient(tmpPath)
	if err != nil {
		steps = append(steps, testStep{
			Name: "Schema validation", OK: false,
			Detail:    err.Error(),
			ElapsedMS: time.Since(t2).Milliseconds(),
		})
		return nil, steps, "schema invalid"
	}
	steps = append(steps, testStep{
		Name: "Schema validation", OK: true,
		Detail:    fmt.Sprintf("client_id=%s transport=%s", cfg.ClientID, cfg.Transport.Type),
		ElapsedMS: time.Since(t2).Milliseconds(),
	})

	// step 3: build transport + runtime + activity-capture logger + preflight
	t3 := time.Now()
	tr, err := buildTransport(cfg)
	if err != nil {
		steps = append(steps, testStep{
			Name: "Preflight (transport + key match)", OK: false,
			Detail:    "build transport: " + err.Error(),
			ElapsedMS: time.Since(t3).Milliseconds(),
		})
		return nil, steps, "transport build failed"
	}
	rt, err := clientruntime.New(cfg, tr)
	if err != nil {
		closeIfPossible(tr)
		steps = append(steps, testStep{
			Name: "Preflight (transport + key match)", OK: false,
			Detail:    "runtime init: " + err.Error(),
			ElapsedMS: time.Since(t3).Milliseconds(),
		})
		return nil, steps, "runtime init failed"
	}

	// Install our activity-capture logger so socks.connect, session.open,
	// session.close events flow into the per-session activity log.
	actLog := newActivityLog(maxActivityLog)
	captureLogger := slog.New(&captureHandler{
		inner: slog.New(slog.NewTextHandler(io.Discard, nil)).Handler(),
		log:   actLog,
	})
	rt.SetLogger(captureLogger)

	diagCtx, dcancel := context.WithTimeout(context.Background(), preflightTimeout)
	report := rt.RunStartupDiagnostics(diagCtx)
	dcancel()

	preflightOK := report.Transport.Healthy && report.ProbeOK
	preflightDetail := "AES key matches end-to-end"
	if !preflightOK {
		switch {
		case !report.Transport.Healthy && report.ProbeErr != "":
			preflightDetail = "transport unreachable: " + report.ProbeErr
		case !report.Transport.Healthy:
			preflightDetail = "transport unreachable"
		case report.ProbeErr != "":
			preflightDetail = "probe failed: " + report.ProbeErr
		default:
			preflightDetail = "probe failed (no detail)"
		}
	}
	steps = append(steps, testStep{
		Name: "Preflight (transport + key match)", OK: preflightOK,
		Detail:    preflightDetail,
		ElapsedMS: report.Elapsed.Milliseconds(),
	})
	if !preflightOK {
		_ = rt.Close()
		closeIfPossible(tr)
		return nil, steps, "preflight failed"
	}

	// step 4: start the in-process pump + SOCKS5 listener (persistent)
	pump := clientruntime.NewPump(rt)
	if cfg.CoalesceStepMs > 0 {
		pump.SetCoalesceWindow(time.Duration(cfg.CoalesceStepMs) * time.Millisecond)
	}
	pump.Start()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = pump.Close()
		_ = rt.Close()
		closeIfPossible(tr)
		steps = append(steps, testStep{
			Name: "Bind SOCKS5 listener", OK: false,
			Detail: err.Error(),
		})
		return nil, steps, "listen failed"
	}
	socksAddr := listener.Addr().String()
	socksSrv := socks.NewServer(pump)
	if cfg.Socks.Username != "" {
		socksSrv.SetAuth(socks.AuthConfig{
			Username: cfg.Socks.Username,
			Password: cfg.Socks.Password,
		})
	}

	sess := &tunnelSession{
		cfg:          cfg,
		tr:           tr,
		rt:           rt,
		pump:         pump,
		socksSrv:     socksSrv,
		listener:     listener,
		socksAddr:    socksAddr,
		startedAt:    time.Now(),
		activityLog:  actLog,
		preflightOK:  true,
		preflightMS:  report.Elapsed.Milliseconds(),
		preflightMsg: preflightDetail,
	}

	// Wrap the listener so we count tunnel connections from the browser.
	counted := &countingListener{inner: listener, counter: &sess.connCount}
	go func() {
		if err := socksSrv.Serve(counted); err != nil && !errors.Is(err, net.ErrClosed) {
			log.Printf("socks server stopped: %v", err)
		}
	}()
	steps = append(steps, testStep{
		Name: "SOCKS5 listener ready", OK: true,
		Detail: socksAddr,
	})
	return sess, steps, ""
}

// --- /api/disconnect ---

type disconnectResponse struct {
	OK             bool  `json:"ok"`
	UptimeSec      int64 `json:"uptime_sec"`
	ConnectionsBy  int64 `json:"connections_handled"`
	ActivityEvents int   `json:"activity_events"`
}

func handleDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.active == nil {
		writeJSON(w, http.StatusOK, disconnectResponse{OK: true})
		return
	}
	sess := state.active
	state.active = nil

	resp := disconnectResponse{
		OK:             true,
		UptimeSec:      int64(time.Since(sess.startedAt).Seconds()),
		ConnectionsBy:  sess.connCount.Load(),
		ActivityEvents: sess.activityLog.size(),
	}
	teardown(sess)
	writeJSON(w, http.StatusOK, resp)
	log.Printf("disconnect: uptime=%ds connections=%d", resp.UptimeSec, resp.ConnectionsBy)
}

func teardown(sess *tunnelSession) {
	_ = sess.socksSrv.Close()
	_ = sess.listener.Close()
	_ = sess.pump.Close()
	_ = sess.rt.Close()
	closeIfPossible(sess.tr)
}

// --- /api/status ---

type statusResponse struct {
	Connected      bool   `json:"connected"`
	ClientID       string `json:"client_id,omitempty"`
	TransportT     string `json:"transport_type,omitempty"`
	SocksAddr      string `json:"socks_addr,omitempty"`
	UptimeSec      int64  `json:"uptime_sec,omitempty"`
	Connections    int64  `json:"connections_handled,omitempty"`
	PreflightMS    int64  `json:"preflight_ms,omitempty"`
	ActivityEvents int    `json:"activity_events,omitempty"`
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.active == nil {
		writeJSON(w, http.StatusOK, statusResponse{Connected: false})
		return
	}
	sess := state.active
	writeJSON(w, http.StatusOK, statusResponse{
		Connected:      true,
		ClientID:       sess.cfg.ClientID,
		TransportT:     sess.cfg.Transport.Type,
		SocksAddr:      sess.socksAddr,
		UptimeSec:      int64(time.Since(sess.startedAt).Seconds()),
		Connections:    sess.connCount.Load(),
		PreflightMS:    sess.preflightMS,
		ActivityEvents: sess.activityLog.size(),
	})
}

// --- /api/smoke ---

type smokeResponse struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	HTTPCode  int    `json:"http_code,omitempty"`
	EgressIP  string `json:"egress_ip,omitempty"`
	ElapsedMS int64  `json:"elapsed_ms,omitempty"`
}

func handleSmoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	state.mu.Lock()
	sess := state.active
	state.mu.Unlock()
	if sess == nil {
		writeJSON(w, http.StatusOK, smokeResponse{
			OK:    false,
			Error: "not connected — click Connect first",
		})
		return
	}
	t0 := time.Now()
	egressIP, httpCode, err := smokeCurl(sess.socksAddr, sess.cfg.Socks.Username, sess.cfg.Socks.Password)
	elapsed := time.Since(t0)
	if err != nil {
		writeJSON(w, http.StatusOK, smokeResponse{
			OK:        false,
			Error:     err.Error(),
			ElapsedMS: elapsed.Milliseconds(),
		})
		return
	}
	writeJSON(w, http.StatusOK, smokeResponse{
		OK:        httpCode == http.StatusOK,
		HTTPCode:  httpCode,
		EgressIP:  egressIP,
		ElapsedMS: elapsed.Milliseconds(),
	})
}

// --- /api/activity ---

type activityResponse struct {
	Entries []activityEntry `json:"entries"`
}

func handleActivity(w http.ResponseWriter, r *http.Request) {
	state.mu.Lock()
	sess := state.active
	state.mu.Unlock()
	if sess == nil {
		writeJSON(w, http.StatusOK, activityResponse{Entries: nil})
		return
	}
	since := int64(0)
	if v := r.URL.Query().Get("since"); v != "" {
		_, _ = fmt.Sscanf(v, "%d", &since)
	}
	writeJSON(w, http.StatusOK, activityResponse{
		Entries: sess.activityLog.snapshot(since),
	})
}

// --- /api/launch-browser ---

type launchBrowserRequest struct {
	URL string `json:"url,omitempty"` // optional landing page; default = api.ipify.org
}

type launchBrowserResponse struct {
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	Browser  string `json:"browser,omitempty"`  // friendly name of the browser launched
	DataDir  string `json:"data_dir,omitempty"` // tmp profile dir
	PID      int    `json:"pid,omitempty"`
	Launched string `json:"launched_url,omitempty"`
}

// handleLaunchBrowser spawns a fresh Chromium-family browser process
// pre-configured to use the active tunnel's SOCKS5 proxy. The new
// window uses a throwaway profile under /tmp so it doesn't share
// cookies / extensions / history with the user's normal browser.
// First page loaded is api.ipify.org so the user gets instant visual
// confirmation that traffic is routing through the VPS.
func handleLaunchBrowser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	state.mu.Lock()
	sess := state.active
	state.mu.Unlock()
	if sess == nil {
		writeJSON(w, http.StatusOK, launchBrowserResponse{
			OK: false, Error: "not connected — click Connect first",
		})
		return
	}

	var req launchBrowserRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	landing := strings.TrimSpace(req.URL)
	if landing == "" {
		landing = "https://api.ipify.org/"
	} else {
		// Defense: only allow http/https schemes; reject anything else
		// so a malicious request can't get the playground to launch a
		// browser pointed at file:// or chrome://flags.
		u, err := url.Parse(landing)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			writeJSON(w, http.StatusOK, launchBrowserResponse{
				OK: false, Error: "landing URL must be http(s) — got: " + landing,
			})
			return
		}
	}

	binPath, friendly := findChromiumBrowser()
	if binPath == "" {
		writeJSON(w, http.StatusOK, launchBrowserResponse{
			OK:    false,
			Error: "no Chromium-family browser found (looked for Chrome, Edge, Brave, Chromium, Arc under /Applications)",
		})
		return
	}

	// Fresh profile so the launched window has no cookies / extensions
	// from the user's normal browser session. Each launch gets its own
	// dir, so multiple parallel launches don't collide.
	dataDir, err := os.MkdirTemp("", fmt.Sprintf("bg-playground-%s-*", strings.ReplaceAll(strings.ToLower(friendly), " ", "")))
	if err != nil {
		writeJSON(w, http.StatusOK, launchBrowserResponse{
			OK: false, Error: "create tmp profile dir: " + err.Error(),
		})
		return
	}

	args := []string{
		"--user-data-dir=" + dataDir,
		// For socks5:// (not socks://) Chrome passes the hostname to the
		// proxy and lets the proxy resolve it — no local DNS lookup. So
		// we don't need --host-resolver-rules to prevent DNS leaks. An
		// earlier version of this code had one, but it triggered Chrome's
		// "unsupported command-line flag" warning banner and may have
		// interfered with internal lookups. Dropped.
		"--proxy-server=socks5://" + sess.socksAddr,
		// UX: skip welcome dialogs that would otherwise interrupt.
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-features=ChromeWhatsNewUI,SignInProfileCreation",
		// Mute Chrome's background chatter so the activity log + page
		// timing measures the actual page, not Chrome's housekeeping.
		// Each one of these would otherwise fire 2–4 s of tunnel traffic
		// on a fresh profile (mtalk push channel, Safe Browsing, GCM,
		// component updates, auto-update checks, ML hint preloading).
		"--disable-background-networking", // Safe Browsing, GCM/mtalk, component update, etc.
		"--disable-component-update",      // chrome auto-update component pulls
		"--disable-sync",                  // chrome sync to accounts.google.com
		"--disable-default-apps",
		"--disable-domain-reliability",
		"--no-pings", // <a ping=...> beacons
		"--disable-features=OptimizationHints,Translate,InterestFeedContentSuggestions,MediaRouter",
		"--metrics-recording-only", // don't upload UMA
		landing,
	}
	// binPath comes from the fixed candidate list in chromePath(); args are
	// internally constructed string literals plus the operator-supplied
	// landing URL. No untrusted process spawn here.
	cmd := exec.Command(binPath, args...) //nolint:gosec // G204: binPath constrained, args from local literals + validated URL
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dataDir)
		writeJSON(w, http.StatusOK, launchBrowserResponse{
			OK: false, Error: "spawn browser: " + err.Error(),
		})
		return
	}
	// Let it run independently — don't wait. The user closes the window
	// when done; the temp profile dir leaks but it's harmless under
	// /tmp and Mac cleans up on reboot. We log so operators can find
	// and reap them if needed.
	log.Printf("launched %s pid=%d profile=%s landing=%s",
		friendly, cmd.Process.Pid, dataDir, landing)
	go func() {
		_ = cmd.Wait()
		// Clean up the profile dir when the browser exits.
		_ = os.RemoveAll(dataDir)
	}()
	writeJSON(w, http.StatusOK, launchBrowserResponse{
		OK:       true,
		Browser:  friendly,
		DataDir:  dataDir,
		PID:      cmd.Process.Pid,
		Launched: landing,
	})
}

// findChromiumBrowser searches well-known install paths for a
// Chromium-family browser that accepts --proxy-server. Returns the
// executable path and a friendly name, or empty strings if none found.
// macOS-only for now; trivial to extend to Linux/Windows if the
// playground ever runs there.
func findChromiumBrowser() (binPath, friendly string) {
	if runtime.GOOS != "darwin" {
		return "", ""
	}
	candidates := []struct {
		path, name string
	}{
		{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "Google Chrome"},
		{filepath.Join(os.Getenv("HOME"), "Applications", "Google Chrome.app", "Contents", "MacOS", "Google Chrome"), "Google Chrome"},
		{"/Applications/Chromium.app/Contents/MacOS/Chromium", "Chromium"},
		{"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser", "Brave"},
		{"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge", "Microsoft Edge"},
		{"/Applications/Arc.app/Contents/MacOS/Arc", "Arc"},
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c.path); err == nil && !fi.IsDir() {
			return c.path, c.name
		}
	}
	return "", ""
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func smokeCurl(socksAddr, user, pass string) (egressIP string, httpCode int, err error) {
	var auth *proxy.Auth
	if user != "" {
		auth = &proxy.Auth{User: user, Password: pass}
	}
	dialer, err := proxy.SOCKS5("tcp", socksAddr, auth, proxy.Direct)
	if err != nil {
		return "", 0, fmt.Errorf("build SOCKS5 dialer: %w", err)
	}
	contextDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return "", 0, errors.New("SOCKS5 dialer does not support context dial")
	}
	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext:           contextDialer.DialContext,
			DisableKeepAlives:     true,
			ResponseHeaderTimeout: smokeRequestLimit,
		},
		Timeout: smokeRequestLimit,
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, smokeURL, nil)
	if err != nil {
		return "", 0, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return "", resp.StatusCode, err
	}
	return strings.TrimSpace(string(body)), resp.StatusCode, nil
}

func buildTransport(cfg *config.ClientConfig) (transport.ClientTransport, error) {
	switch cfg.Transport.Type {
	case "https", "google":
		return buildHTTPSTransport(cfg)
	case "appsscript":
		return buildAppsScriptTransport(cfg)
	}
	return nil, fmt.Errorf("unknown transport type %q", cfg.Transport.Type)
}

func buildHTTPSTransport(cfg *config.ClientConfig) (transport.ClientTransport, error) {
	httpsCfg := httpstransport.Config{
		URL:          cfg.Server.URL,
		HealthURL:    cfg.Transport.OptionString("health_url"),
		FrontingHost: httpstransport.SanitizeFrontingHost(cfg.Transport.OptionString("fronting_host")),
	}
	if path := strings.TrimSpace(cfg.Transport.OptionString("pinned_roots_path")); path != "" {
		pem, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("https transport: read pinned_roots_path %q: %w", path, err)
		}
		httpsCfg.PinnedRootsPEM = pem
	}
	return httpstransport.New(httpsCfg)
}

func buildAppsScriptTransport(cfg *config.ClientConfig) (transport.ClientTransport, error) {
	if cfg.Server.URL != "" {
		return nil, fmt.Errorf("appsscript transport: server.url must be empty in appsscript mode (got %q)", cfg.Server.URL)
	}
	scriptKeys, err := config.ParseScriptKeys(cfg.Transport.Options["script_keys"])
	if err != nil {
		return nil, fmt.Errorf("appsscript transport: %w", err)
	}
	if len(scriptKeys) == 0 {
		return nil, fmt.Errorf("appsscript transport: zero script_keys after parse")
	}
	return appsscript.New(appsscript.Config{
		ScriptKeys:     config.ScriptKeyIDs(scriptKeys),
		ScriptAccounts: config.ScriptKeyAccounts(scriptKeys),
		Fronting: appsscript.FrontingConfig{
			GoogleIP: cfg.Transport.OptionString("google_host"),
			SNIHosts: splitAndTrim(cfg.Transport.OptionString("sni")),
		},
	})
}

func splitAndTrim(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func closeIfPossible(x any) {
	if c, ok := x.(interface{ Close() error }); ok {
		_ = c.Close()
	}
}

// countingListener wraps a net.Listener so every Accept bumps a counter.
// Used to surface "connections handled so far" in /api/status without
// hooking inside the socks package.
type countingListener struct {
	inner   net.Listener
	counter *atomic.Int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.inner.Accept()
	if err == nil {
		l.counter.Add(1)
	}
	return c, err
}
func (l *countingListener) Close() error   { return l.inner.Close() }
func (l *countingListener) Addr() net.Addr { return l.inner.Addr() }

// --- activity log + slog capture ---

type activityEntry struct {
	ID        int64     `json:"id"`
	Time      time.Time `json:"time"`
	Kind      string    `json:"kind"`             // "connect" | "open" | "close" | "error"
	Target    string    `json:"target,omitempty"` // host:port from socks/session events
	Message   string    `json:"message"`
	Error     string    `json:"error,omitempty"`
	DurationM int64     `json:"duration_ms,omitempty"`
}

// activityLog is a bounded ring of recent tunnel events extracted from
// the runtime's slog stream.
type activityLog struct {
	mu      sync.Mutex
	entries []activityEntry
	max     int
	next    int64 // monotonic id
}

func newActivityLog(max int) *activityLog {
	return &activityLog{max: max}
}

func (l *activityLog) append(kind, msg, target, errStr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.next++
	l.entries = append(l.entries, activityEntry{
		ID:      l.next,
		Time:    time.Now(),
		Kind:    kind,
		Target:  target,
		Message: msg,
		Error:   errStr,
	})
	if len(l.entries) > l.max {
		l.entries = l.entries[len(l.entries)-l.max:]
	}
}

func (l *activityLog) snapshot(sinceID int64) []activityEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]activityEntry, 0, len(l.entries))
	for _, e := range l.entries {
		if e.ID > sinceID {
			out = append(out, e)
		}
	}
	return out
}

func (l *activityLog) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// captureHandler is a slog.Handler that scans every record for events
// we care about (socks.connect, session.open, session.upstream_eof,
// socks.connect_failed, etc.) and appends them to the activity log.
// Other records are silently discarded — the playground is the only
// caller, and the inner handler discards to keep the playground's
// stdout quiet.
type captureHandler struct {
	inner slog.Handler
	log   *activityLog
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &captureHandler{inner: h.inner.WithAttrs(attrs), log: h.log}
}
func (h *captureHandler) WithGroup(name string) slog.Handler {
	return &captureHandler{inner: h.inner.WithGroup(name), log: h.log}
}
func (h *captureHandler) Handle(ctx context.Context, r slog.Record) error {
	// Always pass through to the inner handler (discards by default).
	_ = h.inner.Handle(ctx, r)

	// Extract interesting attrs.
	var target, errStr string
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "target":
			target = a.Value.String()
		case "error":
			errStr = a.Value.String()
		}
		return true
	})

	switch r.Message {
	case "socks.connect", "session.open":
		h.log.append("open", r.Message, target, errStr)
	case "session.upstream_eof", "session.close", "socks.disconnect":
		h.log.append("close", r.Message, target, errStr)
	case "socks.connect_failed", "session.reset", "pump.exchange_failed":
		h.log.append("error", r.Message, target, errStr)
	}
	return nil
}
