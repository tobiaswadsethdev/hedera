// Package server implements the agent's HTTP and WebSocket API: the remote
// listener (Tailscale only, TLS, pairing tokens) and the local listener
// (loopback, local admin token).
package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/tobiaswadsethdev/hedera/agent/internal/pairing"
	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
	"github.com/tobiaswadsethdev/hedera/agent/internal/push"
	"github.com/tobiaswadsethdev/hedera/agent/internal/session"
)

// LocalPrincipal identifies connections authenticated with the local token.
const LocalPrincipal = "local"

// PairInfo is what goes into a pairing URL besides the code.
type PairInfo struct {
	Addrs       []netip.Addr
	DNSName     string
	Port        int
	Fingerprint string
}

// Server holds the dependencies shared by both listeners.
type Server struct {
	Manager    *session.Manager
	Pairings   *pairing.Store
	LocalToken string
	MachineID  string
	BootID     string
	Hostname   string
	Version    string
	Log        *slog.Logger
	// Push delivers notifications through Radix; nil disables push.
	Push *push.Pusher
	// PairInfo describes how clients reach the remote listener right now.
	PairInfo func() PairInfo
	// AllowLoopbackPeers lets the remote handler accept loopback peers, for
	// the loopback TLS listener used in development and tests.
	AllowLoopbackPeers bool

	mu      sync.Mutex
	conns   map[*conn]struct{}
	limiter failureLimiter
}

func (s *Server) init() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conns == nil {
		s.conns = map[*conn]struct{}{}
	}
}

// RemoteHandler serves the Tailscale listener.
func (s *Server) RemoteHandler() http.Handler {
	s.init()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/ws", func(w http.ResponseWriter, r *http.Request) {
		tok, ok := bearer(r)
		if !ok {
			unauthorized(w)
			return
		}
		id, ok := s.Pairings.Authenticate(tok)
		if !ok {
			unauthorized(w)
			return
		}
		s.serveWS(w, r, id)
	})
	mux.HandleFunc("POST /v1/pair", s.handlePair)
	return s.peerCheck(mux, func(a netip.Addr) bool {
		return IsTailscale(a) || (s.AllowLoopbackPeers && a.IsLoopback())
	})
}

// LocalHandler serves the loopback listener.
func (s *Server) LocalHandler() http.Handler {
	s.init()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/ws", s.requireLocal(func(w http.ResponseWriter, r *http.Request) {
		s.serveWS(w, r, LocalPrincipal)
	}))
	mux.HandleFunc("POST /v1/local/pair-codes", s.requireLocal(s.handleNewCode))
	mux.HandleFunc("GET /v1/local/pair-codes/{id}/wait", s.requireLocal(s.handleWaitCode))
	mux.HandleFunc("GET /v1/local/pairings", s.requireLocal(s.handleListPairings))
	mux.HandleFunc("DELETE /v1/local/pairings/{id}", s.requireLocal(s.handleRevoke))
	mux.HandleFunc("POST /v1/hook", s.handleHook)
	mux.HandleFunc("GET /v1/local/push", s.requireLocal(s.handlePushRoutes))
	mux.HandleFunc("POST /v1/local/push/test", s.requireLocal(s.handlePushTest))
	return s.peerCheck(mux, netip.Addr.IsLoopback)
}

// HookSessionHeader names the session a hook event belongs to.
const HookSessionHeader = "X-Hedera-Session"

// handleHook receives Claude Code hook events from `hedera hook`. It is
// authenticated with the session's own hook token, so a process can only
// report events for the session it runs in.
func (s *Server) handleHook(w http.ResponseWriter, r *http.Request) {
	tok, _ := bearer(r)
	sess, err := s.Manager.Get(r.Header.Get(HookSessionHeader))
	// Compare against a dummy when the session is unknown so both failures
	// take the same path.
	want := "hht_no-such-session"
	if err == nil {
		want = sess.HookToken()
	}
	got, exp := sha256.Sum256([]byte(tok)), sha256.Sum256([]byte(want))
	if subtle.ConstantTimeCompare(got[:], exp[:]) != 1 || err != nil {
		unauthorized(w)
		return
	}
	var ev session.HookEvent
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&ev); err != nil || ev.HookEventName == "" {
		http.Error(w, "bad hook payload", http.StatusBadRequest)
		return
	}
	s.Log.Debug("hook", "session", sess.ID(), "event", ev.HookEventName, "type", ev.NotificationType)
	sess.ApplyHook(ev)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) peerCheck(next http.Handler, allowed func(netip.Addr) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ap, err := netip.ParseAddrPort(r.RemoteAddr)
		if err != nil || !allowed(ap.Addr().Unmap()) {
			s.Log.Warn("rejected peer", "remote", r.RemoteAddr)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireLocal(h http.HandlerFunc) http.HandlerFunc {
	want := sha256.Sum256([]byte(s.LocalToken))
	return func(w http.ResponseWriter, r *http.Request) {
		tok, _ := bearer(r)
		got := sha256.Sum256([]byte(tok))
		if s.LocalToken == "" || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			unauthorized(w)
			return
		}
		h(w, r)
	}
}

func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	scheme, tok, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || tok == "" {
		return "", false
	}
	return tok, true
}

func unauthorized(w http.ResponseWriter) {
	w.WriteHeader(http.StatusUnauthorized)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) serveWS(w http.ResponseWriter, r *http.Request, principal string) {
	offered := false
	for _, v := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(v, ",") {
			if strings.TrimSpace(p) == proto.Subprotocol {
				offered = true
			}
		}
	}
	if !offered {
		http.Error(w, "the hedera.v1 subprotocol is required", http.StatusBadRequest)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{proto.Subprotocol}})
	if err != nil {
		s.Log.Debug("websocket accept failed", "err", err)
		return
	}
	c := newConn(s, ws, principal)
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()
	s.Log.Info("client connected", "principal", principal, "remote", r.RemoteAddr)
	c.run(r.Context())
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
	s.Log.Info("client disconnected", "principal", principal, "remote", r.RemoteAddr)
}

// CloseConnections closes every WebSocket authenticated as principal ("" for
// all) with the given close code.
func (s *Server) CloseConnections(principal string, code websocket.StatusCode, reason string) {
	s.mu.Lock()
	var victims []*conn
	for c := range s.conns {
		if principal == "" || c.principal == principal {
			victims = append(victims, c)
		}
	}
	s.mu.Unlock()
	for _, c := range victims {
		c.close(code, reason)
	}
}

type pairRequest struct {
	Code       string `json:"code"`
	DeviceName string `json:"device_name"`
}

type pairResponse struct {
	PairingID string `json:"pairing_id"`
	Token     string `json:"token"`
	MachineID string `json:"machine_id"`
	Name      string `json:"name"`
}

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	ip := peerIP(r)
	if !s.limiter.allow(ip, time.Now()) {
		http.Error(w, "too many failed attempts", http.StatusTooManyRequests)
		return
	}
	var req pairRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil || req.Code == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	p, token, err := s.Pairings.Redeem(req.Code, req.DeviceName)
	if errors.Is(err, pairing.ErrBadCode) {
		s.limiter.fail(ip, time.Now())
		s.Log.Warn("pairing attempt with a bad code", "remote", r.RemoteAddr)
		unauthorized(w)
		return
	}
	if err != nil {
		s.Log.Error("pairing failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.Log.Info("paired", "pairing", p.ID, "device", p.DeviceName)
	writeJSON(w, http.StatusOK, pairResponse{PairingID: p.ID, Token: token, MachineID: s.MachineID, Name: s.Hostname})
}

func peerIP(r *http.Request) netip.Addr {
	ap, _ := netip.ParseAddrPort(r.RemoteAddr)
	return ap.Addr().Unmap()
}

func (s *Server) handleNewCode(w http.ResponseWriter, r *http.Request) {
	info := s.PairInfo()
	if len(info.Addrs) == 0 {
		http.Error(w, "no Tailscale address yet; is Tailscale running?", http.StatusServiceUnavailable)
		return
	}
	id, code, expires := s.Pairings.NewCode()
	writeJSON(w, http.StatusOK, map[string]any{
		"code_id":    id,
		"url":        PairURL(s.MachineID, s.Hostname, info, code),
		"expires_at": proto.FormatTime(expires),
	})
}

// PairURL builds the hedera://pair URL shown as a QR code.
func PairURL(machineID, name string, info PairInfo, code string) string {
	q := url.Values{}
	q.Set("v", "1")
	q.Set("m", machineID)
	q.Set("n", name)
	for _, a := range info.Addrs {
		q.Add("a", a.String())
	}
	if info.DNSName != "" {
		q.Set("d", info.DNSName)
	}
	q.Set("p", strconv.Itoa(info.Port))
	q.Set("fp", info.Fingerprint)
	q.Set("c", code)
	return "hedera://pair?" + q.Encode()
}

func (s *Server) handleWaitCode(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	st, err := s.Pairings.WaitCode(ctx, r.PathValue("id"))
	if errors.Is(err, pairing.ErrNotFound) {
		http.Error(w, "no such code", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleListPairings(w http.ResponseWriter, r *http.Request) {
	type item struct {
		ID         string `json:"id"`
		DeviceName string `json:"device_name"`
		CreatedAt  string `json:"created_at"`
		LastSeenAt string `json:"last_seen_at,omitempty"`
	}
	items := []item{}
	for _, p := range s.Pairings.List() {
		it := item{ID: p.ID, DeviceName: p.DeviceName, CreatedAt: proto.FormatTime(p.CreatedAt)}
		if !p.LastSeenAt.IsZero() {
			it.LastSeenAt = proto.FormatTime(p.LastSeenAt)
		}
		items = append(items, it)
	}
	writeJSON(w, http.StatusOK, map[string]any{"pairings": items})
}

func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Pairings.Revoke(id); errors.Is(err, pairing.ErrNotFound) {
		http.Error(w, "no such pairing", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.CloseConnections(id, proto.CloseRevoked, "revoked")
	if s.Push != nil {
		s.Push.Remove(id)
	}
	s.Log.Info("pairing revoked", "pairing", id)
	w.WriteHeader(http.StatusNoContent)
}

// failureLimiter allows at most maxFailures failed pairing attempts per peer
// address within failureWindow.
type failureLimiter struct {
	mu       sync.Mutex
	failures map[netip.Addr][]time.Time
}

const (
	maxFailures   = 10
	failureWindow = 10 * time.Minute
)

func (l *failureLimiter) recent(ip netip.Addr, now time.Time) []time.Time {
	kept := l.failures[ip][:0]
	for _, t := range l.failures[ip] {
		if now.Sub(t) < failureWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.failures, ip)
		return nil
	}
	l.failures[ip] = kept
	return kept
}

func (l *failureLimiter) allow(ip netip.Addr, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failures == nil {
		return true
	}
	return len(l.recent(ip, now)) < maxFailures
}

func (l *failureLimiter) fail(ip netip.Addr, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failures == nil {
		l.failures = map[netip.Addr][]time.Time{}
	}
	l.failures[ip] = append(l.recent(ip, now), now)
}

func (s *Server) handlePushRoutes(w http.ResponseWriter, r *http.Request) {
	routes := []push.RouteStatus{}
	if s.Push != nil {
		routes = s.Push.Routes()
	}
	writeJSON(w, http.StatusOK, map[string]any{"routes": routes})
}

func (s *Server) handlePushTest(w http.ResponseWriter, r *http.Request) {
	if s.Push == nil {
		http.Error(w, "push is disabled", http.StatusServiceUnavailable)
		return
	}
	n := s.Push.Enqueue(push.Notification{
		EventID:  s.BootID + "-test-" + strconv.FormatInt(time.Now().UnixMilli(), 10),
		Kind:     push.KindTest,
		Title:    "Hedera",
		Subtitle: s.Hostname,
		Body:     "Test notification from " + s.Hostname,
	})
	writeJSON(w, http.StatusOK, map[string]any{"queued": n})
}
