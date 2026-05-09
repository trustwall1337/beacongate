package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/trustwall1337/beacongate/engine/crypto"
	"github.com/trustwall1337/beacongate/engine/protocol"
	"github.com/trustwall1337/beacongate/engine/replay"
)

// tunnelMaxBody bounds the inbound batch size we'll read before
// rejecting. The replay store's SkewMax (engine/replay) owns the
// timestamp-window tolerance; nothing else in the handler caps the
// body.
const tunnelMaxBody = 4 * 1024 * 1024

func (s *Server) handleTunnel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Plan D1: per-IP rate cap. Lifted before any per-request work
	// so a flood doesn't load the server beyond reading the IP.
	ip := remoteIP(r.RemoteAddr)
	if !s.tunnelLimiter.Allow(ip, time.Now()) {
		s.log().Warn("tunnel.rate_limited", "remote_ip", ip)
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, tunnelMaxBody))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Peek the cleartext client_id and route to the per-client
	// Sealer BEFORE any AEAD work. In multi-tenant mode an unknown
	// client_id is rejected here; in single-tenant mode the
	// registry returns the shared Sealer for any id (legacy
	// behaviour preserved).
	//
	// **Status-code uniformity (security):** every failure mode in
	// this handler — malformed wire header (PeekClientID), unknown
	// client_id (Lookup miss), and AEAD authentication failure
	// (sealer.Open) — returns 401. An external observer probing the
	// endpoint cannot distinguish "wrong wire version" from "wrong
	// key" from "client_id not in allowlist", so the auth gate adds
	// no fingerprintable signal. Detail goes to server logs only.
	peekedID, err := crypto.PeekClientID(body)
	if err != nil {
		s.log().Warn("tunnel.bad_header",
			"remote_addr", r.RemoteAddr,
			"error", err.Error())
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	sealer := s.sealers.Lookup(peekedID)
	if sealer == nil {
		s.log().Warn("tunnel.unknown_client",
			"remote_addr", r.RemoteAddr,
			"client_id", peekedID)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	batch, err := sealer.Open(body)
	if err != nil {
		// C4: never echo crypto error detail to the wire — it leaks state
		// useful for fingerprinting (was the body too short? wrong tag?).
		// Detail goes to server logs, not the client.
		s.log().Warn("tunnel.auth_failed",
			"remote_addr", r.RemoteAddr,
			"client_id", peekedID,
			"error", err.Error())
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Plan B4: consult the replay store. The store handles the
	// timestamp-window check, dedup cache, and idempotent retry
	// (cached response replay) in one call.
	now := time.Now()
	decision, cachedResponse := s.replayStore.Check(batch.ClientID, batch.ReplayID, batch.Timestamp, now)
	switch decision {
	case replay.Accept:
		// New batch — process below. The handler will call
		// RecordResponse after building the response.
	case replay.DuplicateProcessed:
		// Idempotent retry. Return the cached response verbatim.
		// The wire bytes are already encrypted (the cache stores
		// the post-Seal bytes), so just write them back.
		s.log().Info("tunnel.replay_idempotent",
			"client_id", batch.ClientID,
			"size", len(cachedResponse))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(cachedResponse)
		return
	case replay.Replayed:
		s.log().Warn("tunnel.replay_rejected",
			"remote_addr", r.RemoteAddr,
			"client_id", batch.ClientID)
		http.Error(w, "replayed", http.StatusBadRequest)
		return
	case replay.StaleTimestamp:
		s.log().Warn("tunnel.stale_envelope",
			"remote_addr", r.RemoteAddr,
			"client_id", batch.ClientID,
			"skew", now.Sub(batch.Timestamp).String())
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	case replay.RatePressure:
		s.log().Warn("tunnel.rate_pressure",
			"remote_addr", r.RemoteAddr,
			"client_id", batch.ClientID)
		http.Error(w, "rate pressure", http.StatusTooManyRequests)
		return
	default:
		s.log().Error("tunnel.unknown_replay_decision",
			"decision", decision.String())
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	env, err := protocol.DecodeEnvelope(batch.Plaintext)
	if err != nil {
		s.log().Warn("tunnel.bad_envelope",
			"remote_addr", r.RemoteAddr,
			"error", err.Error())
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Cleartext header's client_id must match the JSON envelope's
	// (the AAD already binds the cleartext, but the JSON envelope is
	// inside the AEAD — assert they match so a future code path can't
	// quietly read the wrong identity).
	if env.ClientID != batch.ClientID {
		s.log().Warn("tunnel.client_id_mismatch",
			"wire_id", batch.ClientID,
			"envelope_id", env.ClientID)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	respMsgs := s.processBatch(r.Context(), env)
	// Pick the drain window based on the inbound batch shape:
	//   - Probe-only (idle) batches  → longPollWindow (8s default).
	//     Server holds for upstream-originated data so a fresh response
	//     can ship without waiting for the client's next POST.
	//   - Batches carrying DATA or OPEN → activeDrainWindow (1s default).
	//     For DATA batches the wait gives the upstream's response a
	//     chance to fold back into the SAME POST that carried the
	//     request — saves a full Apps Script round-trip per logical
	//     SOCKS request. For OPEN-bearing batches the wait gives the
	//     *next* POST's DATA-ClientHello room to arrive, get forwarded
	//     to the just-dialed upstream, and have the upstream's
	//     ServerHello drained back through this POST — folding the TLS
	//     handshake's first leg into one Apps Script call instead of
	//     two. Both waits short-circuit on the per-client signal, so a
	//     fast upstream / quickly-following ClientHello returns
	//     immediately; the 1s ceiling only fires on legs that produce
	//     no upstream response (e.g. TLS 1.3 client Finished) or on
	//     OPEN+idle patterns where the user opens a session and
	//     doesn't write to it. Late responses (>1s upstream RTT) flow
	//     back on the client's standing idle long-poll worker without
	//     waiting for the next active POST.
	//   - All other active batches (CLOSE-only, RESET, OPEN+CLOSE, etc.)
	//     → short drainWindow (25ms). These tear sessions down or have
	//     no plausible same-POST response to wait for; the legacy
	//     "return promptly" behaviour applies.
	longPoll := isIdleBatch(env)
	wantActiveDrain := hasActiveDrainPayload(env)
	s.mu.Lock()
	var window time.Duration
	switch {
	case longPoll:
		window = s.longPollWindow
	case wantActiveDrain && s.activeDrainWindow > 0:
		window = s.activeDrainWindow
	default:
		window = s.drainWindow
	}
	s.mu.Unlock()
	respMsgs = append(respMsgs, s.collectUpstreamData(r.Context(), env.ClientID, window)...)
	if len(respMsgs) == 0 {
		respMsgs = append(respMsgs, protocol.Message{
			Type: protocol.MessageTypeProbe, ProbeID: "noop",
			Status:            "ok",
			SupportedVersions: []protocol.Version{{Major: 1, Minor: 1}},
		})
	}

	// If the request was canceled while we were holding (long-poll), stop here:
	// the client has already moved on, and writing now would send data to a
	// dead connection (data was *not* drained because the wait honored ctx).
	if r.Context().Err() != nil {
		return
	}

	out := protocol.Envelope{
		Version:     protocol.Version{Major: 1, Minor: 1},
		ClientID:    s.serverID,
		Compression: protocol.CompressionNone,
		Messages:    respMsgs,
	}
	plain, err := protocol.EncodeEnvelope(out)
	if err != nil {
		http.Error(w, "encode response: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Seal the response under the server's own client_id, using the
	// SAME per-client Sealer that authenticated the request. In
	// multi-tenant mode this means the friend's per-friend master
	// key is what derives the outbound AEAD — the friend's client
	// derives the matching key from the same master key on its end.
	// Per-client key derivation means responses use a *different*
	// AEAD key from inbound requests (different cleartext id →
	// different HKDF info), preventing response replay on the
	// request leg.
	cipher, err := sealer.Seal(s.serverID, plain)
	if err != nil {
		http.Error(w, "seal", http.StatusInternalServerError)
		return
	}
	// Cache the sealed response bytes so a benign retry of this same
	// batch (transport-level failover, e.g. appsscript deployment
	// failover) can return the cached bytes verbatim instead of
	// reprocessing or being rejected as REPLAYED. See plan B4.
	s.replayStore.RecordResponse(batch.ClientID, batch.ReplayID, cipher, now)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(cipher)
}

func (s *Server) processBatch(ctx context.Context, env protocol.Envelope) []protocol.Message {
	var resp []protocol.Message
	for i := range env.Messages {
		m := env.Messages[i]
		switch m.Type {
		case protocol.MessageTypeOpen:
			resp = append(resp, s.handleOpen(ctx, env.ClientID, m)...)
		case protocol.MessageTypeData:
			resp = append(resp, s.handleData(env.ClientID, m)...)
		case protocol.MessageTypeClose:
			resp = append(resp, s.handleClose(env.ClientID, m)...)
		case protocol.MessageTypeReset:
			s.handleReset(env.ClientID, m)
		case protocol.MessageTypePing:
			resp = append(resp, protocol.Message{
				Type: protocol.MessageTypePing, SessionID: m.SessionID, Nonce: m.Nonce,
			})
		case protocol.MessageTypeProbe:
			resp = append(resp, protocol.Message{
				Type:              protocol.MessageTypeProbe,
				ProbeID:           m.ProbeID,
				Status:            "ok",
				SupportedVersions: []protocol.Version{{Major: 1, Minor: 1}},
				SelectedVersion:   &protocol.Version{Major: 1, Minor: 1},
			})
		}
	}
	return resp
}

func (s *Server) handleOpen(ctx context.Context, clientID string, m protocol.Message) []protocol.Message {
	target := protocol.Target{}
	if m.Target != nil {
		target = *m.Target
	}
	if existing := s.lookup(clientID, m.SessionID); existing != nil {
		return []protocol.Message{{
			Type: protocol.MessageTypeReset, SessionID: m.SessionID,
			Code:   "SESSION_EXISTS",
			Reason: "session id already open",
		}}
	}
	// C3: per-client session cap. A misbehaving client cannot exhaust the
	// server by opening unlimited sessions; quota error gets a stable code
	// the client can surface to its own SOCKS reply.
	s.mu.Lock()
	limit := s.maxSessionsPerClient
	live := len(s.byClient[clientID])
	s.mu.Unlock()
	if limit > 0 && live >= limit {
		s.log().Warn("session.quota_exceeded",
			"client_id", clientID,
			"session_id", m.SessionID,
			"live", live, "limit", limit)
		return []protocol.Message{{
			Type: protocol.MessageTypeReset, SessionID: m.SessionID,
			Code:   "POLICY_DENIED",
			Reason: "per-client session limit reached",
		}}
	}
	if d := s.currentPolicy().Evaluate(target); !d.Allowed {
		s.log().Warn("session.policy_denied",
			"client_id", clientID,
			"session_id", m.SessionID,
			"target", net.JoinHostPort(target.Host, fmt.Sprintf("%d", target.Port)),
			"reason", d.Reason)
		return []protocol.Message{{
			Type: protocol.MessageTypeReset, SessionID: m.SessionID,
			Code:   "POLICY_DENIED",
			Reason: d.Reason,
		}}
	}
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := s.dial(dialCtx, target)
	if err != nil {
		// C4: dial errors can carry internal IPs / DNS state. Log full
		// detail server-side; ship a stable code to the client.
		code := classifyDialError(err)
		level := slog.LevelInfo
		if code == "blocked" {
			level = slog.LevelWarn // SSRF guard caught this — operator should see it
		}
		s.log().Log(ctx, level, "session.dial_failed",
			"client_id", clientID,
			"session_id", m.SessionID,
			"target", net.JoinHostPort(target.Host, fmt.Sprintf("%d", target.Port)),
			"code", code,
			"error", err.Error())
		return []protocol.Message{{
			Type: protocol.MessageTypeReset, SessionID: m.SessionID,
			Code:   "DIAL_FAILED",
			Reason: code,
		}}
	}
	ss := &serverSession{
		id:           m.SessionID,
		clientID:     clientID,
		target:       target,
		conn:         conn,
		lastActivity: time.Now(),
	}
	s.register(ss)
	s.log().Info("session.open",
		"client_id", clientID,
		"session_id", m.SessionID,
		"target", net.JoinHostPort(target.Host, fmt.Sprintf("%d", target.Port)))
	go s.readUpstream(ss)
	return nil
}

// remoteIP extracts the host portion of an http.Request.RemoteAddr.
// Used as the per-IP key for the tunnel rate limiter.
func remoteIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

// classifyDialError maps an upstream dial error to a small, stable string
// the client can react to without learning the server's network shape.
func classifyDialError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "target address is unsafe"):
		return "blocked"
	case strings.Contains(msg, "no such host"), strings.Contains(msg, "lookup"):
		return "dns"
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "deadline"):
		return "timeout"
	case strings.Contains(msg, "refused"):
		return "refused"
	default:
		return "unreachable"
	}
}

func (s *Server) handleData(clientID string, m protocol.Message) []protocol.Message {
	ss := s.lookup(clientID, m.SessionID)
	if ss == nil {
		return []protocol.Message{{
			Type: protocol.MessageTypeReset, SessionID: m.SessionID,
			Code: "INVALID_STATE", Reason: "no such session",
		}}
	}
	ss.mu.Lock()
	expected := ss.nextRecvSeq
	if m.Seq == nil || *m.Seq != expected {
		got := uint64(0)
		if m.Seq != nil {
			got = *m.Seq
		}
		ss.mu.Unlock()
		ss.terminate(fmt.Errorf("bad sequence: want %d", expected))
		s.unregister(clientID, m.SessionID)
		s.log().Warn("session.bad_sequence",
			"client_id", clientID,
			"session_id", m.SessionID,
			"expected_seq", expected, "got_seq", got)
		return []protocol.Message{{
			Type: protocol.MessageTypeReset, SessionID: m.SessionID,
			Code: "BAD_SEQUENCE", Reason: "out of order DATA",
		}}
	}
	ss.nextRecvSeq++
	ss.lastActivity = time.Now()
	ss.mu.Unlock()
	payload := m.Data
	if m.Compressed {
		raw, err := protocol.DecompressData(payload)
		if err != nil {
			ss.terminate(err)
			s.unregister(clientID, m.SessionID)
			return []protocol.Message{{
				Type: protocol.MessageTypeReset, SessionID: m.SessionID,
				Code: "PEER_ERROR", Reason: "decompress failed",
			}}
		}
		payload = raw
	}
	if err := ss.writeUpstream(payload); err != nil {
		ss.terminate(err)
		s.unregister(clientID, m.SessionID)
		// C4: don't echo upstream errno detail to the client.
		return []protocol.Message{{
			Type: protocol.MessageTypeReset, SessionID: m.SessionID,
			Code: "PEER_ERROR", Reason: "upstream write failed",
		}}
	}
	return nil
}

func (s *Server) handleClose(clientID string, m protocol.Message) []protocol.Message {
	ss := s.lookup(clientID, m.SessionID)
	if ss == nil {
		return nil
	}
	ss.closeWriteUpstream()
	return nil
}

func (s *Server) handleReset(clientID string, m protocol.Message) {
	ss := s.lookup(clientID, m.SessionID)
	if ss == nil {
		return
	}
	ss.terminate(errors.New("client RESET"))
	s.unregister(clientID, m.SessionID)
}

// readUpstream pumps bytes from the upstream connection into the session's
// pending buffer. It exits when the upstream is closed or errored. After
// each read, it notifies the per-client signal so any in-flight long-poll
// can wake up and ship the bytes immediately.
func (s *Server) readUpstream(ss *serverSession) {
	buf := make([]byte, 16*1024)
	for {
		n, err := ss.conn.Read(buf)
		if n > 0 {
			ss.mu.Lock()
			ss.pending = append(ss.pending, append([]byte(nil), buf[:n]...)...)
			ss.lastActivity = time.Now()
			ss.mu.Unlock()
			s.notify(ss.clientID)
		}
		if err != nil {
			ss.mu.Lock()
			if !ss.localClosed {
				ss.localClosed = true
			}
			if err != io.EOF && ss.upErr == nil {
				ss.upErr = err
			}
			ss.mu.Unlock()
			// Wake long-poll so it observes the terminal state (CLOSE/done).
			s.notify(ss.clientID)
			if err == io.EOF {
				s.log().Info("session.upstream_eof",
					"client_id", ss.clientID, "session_id", ss.id)
			} else {
				s.log().Info("session.upstream_error",
					"client_id", ss.clientID, "session_id", ss.id,
					"error", err.Error())
			}
			return
		}
	}
}

// isIdleBatch reports whether the inbound envelope contains nothing the
// client is in a hurry for. Idle batches are PROBE-only requests the client
// uses purely to keep a path open for server-originated data.
func isIdleBatch(env protocol.Envelope) bool {
	if len(env.Messages) == 0 {
		return false
	}
	for _, m := range env.Messages {
		if m.Type != protocol.MessageTypeProbe {
			return false
		}
	}
	return true
}

// hasActiveDrainPayload reports whether the inbound envelope is a
// candidate for the longer activeDrainWindow.
//
// DATA frames push bytes upstream and can plausibly trigger a return-
// trip response, so they qualify. OPEN frames don't produce upstream
// bytes themselves, but they almost always precede a DATA-ClientHello
// from the same client within tens of milliseconds — by holding the
// OPEN POST in active-drain we give that ClientHello room to arrive
// (on the next POST), be forwarded to the just-dialed upstream, and
// have the upstream's ServerHello drained back into the OPEN POST's
// response. That collapses the TLS handshake's first two legs into
// one Apps Script round-trip.
//
// CLOSE / RESET tear sessions down — there's nothing to wait for, so
// they fall through to the short drainWindow. A batch containing both
// OPEN and CLOSE for the same session also doesn't qualify: by the
// time we reach the drain phase the session is gone.
func hasActiveDrainPayload(env protocol.Envelope) bool {
	hasData := false
	openIDs := map[string]struct{}{}
	closeIDs := map[string]struct{}{}
	for _, m := range env.Messages {
		switch m.Type {
		case protocol.MessageTypeData:
			hasData = true
		case protocol.MessageTypeOpen:
			openIDs[m.SessionID] = struct{}{}
		case protocol.MessageTypeClose, protocol.MessageTypeReset:
			closeIDs[m.SessionID] = struct{}{}
		}
	}
	if hasData {
		return true
	}
	// OPEN qualifies only if the same batch isn't tearing the session
	// back down. An [OPEN, CLOSE] pair for the same session has nothing
	// for active-drain to wait on.
	for id := range openIDs {
		if _, closed := closeIDs[id]; !closed {
			return true
		}
	}
	return false
}

// collectUpstreamData drains pending upstream bytes for the given client
// into a sequence of DATA messages. It blocks up to window for new data,
// waking on the per-client signal channel. The window's caller (the
// tunnel handler) chooses a short value for active batches and the
// long-poll value for idle (probe-only) batches.
//
// IMPORTANT: drain happens only after the wait phase commits to returning,
// so a canceled HTTP request leaves bytes in the session's pending buffer
// for the next request rather than losing them.
func (s *Server) collectUpstreamData(ctx context.Context, clientID string, window time.Duration) []protocol.Message {
	// Long-poll only makes sense if this client has live sessions; otherwise
	// no data will ever arrive and we'd just stall a probe-only request the
	// caller wants a quick answer to (e.g. Runtime.Probe()).
	s.mu.Lock()
	hasSessions := len(s.byClient[clientID]) > 0
	s.mu.Unlock()
	if !hasSessions {
		return nil
	}
	signal := s.signal(clientID)
	// Drain pending immediately first; if there's already data, no waiting.
	if out := s.drainAllForClient(clientID); len(out) > 0 {
		return out
	}
	if window <= 0 {
		return nil
	}
	deadline := time.Now().Add(window)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return s.drainAllForClient(clientID)
		}
		select {
		case <-signal:
			// Data arrived (or session went terminal). Drain on the next loop.
			if out := s.drainAllForClient(clientID); len(out) > 0 {
				return out
			}
			// Spurious wake (already drained by another caller). Keep waiting.
		case <-ctx.Done():
			// Caller gave up. Do NOT drain — the bytes belong to a future
			// request so seq stays consistent.
			return nil
		case <-s.stopCh:
			return s.drainAllForClient(clientID)
		case <-time.After(remaining):
			return s.drainAllForClient(clientID)
		}
	}
}

// drainAllForClient performs one drain pass across every live session for
// the client. It assigns send-seq numbers as it builds DATA messages and
// emits CLOSE for sessions whose upstream finished. No waiting.
func (s *Server) drainAllForClient(clientID string) []protocol.Message {
	s.mu.Lock()
	clients := s.byClient[clientID]
	ids := make([]string, 0, len(clients))
	for id := range clients {
		ids = append(ids, id)
	}
	s.mu.Unlock()

	var out []protocol.Message
	for _, id := range ids {
		ss := s.lookup(clientID, id)
		if ss == nil {
			continue
		}
		chunks, done, _ := ss.drain(s.maxChunk)
		for _, c := range chunks {
			ss.mu.Lock()
			seq := ss.nextSendSeq
			ss.nextSendSeq++
			ss.mu.Unlock()
			seqVal := seq
			out = append(out, buildServerDataMessage(id, &seqVal, c))
		}
		if done {
			out = append(out, protocol.Message{
				Type: protocol.MessageTypeClose, SessionID: id,
			})
			ss.terminate(nil)
			s.unregister(clientID, id)
		}
	}
	return out
}

// buildServerDataMessage mirrors the client's chunk policy: compress chunks
// large enough for gzip to be a net win.
func buildServerDataMessage(sessID string, seq *uint64, chunk []byte) protocol.Message {
	if len(chunk) >= protocol.CompressThreshold {
		if compressed, err := protocol.CompressData(chunk); err == nil && len(compressed) < len(chunk) {
			return protocol.Message{
				Type:       protocol.MessageTypeData,
				SessionID:  sessID,
				Seq:        seq,
				Data:       compressed,
				Compressed: true,
			}
		}
	}
	return protocol.Message{
		Type:      protocol.MessageTypeData,
		SessionID: sessID,
		Seq:       seq,
		Data:      chunk,
	}
}
