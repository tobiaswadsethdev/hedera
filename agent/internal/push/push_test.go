package push

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
	"github.com/tobiaswadsethdev/hedera/agent/internal/session"
)

// fakeRadix records events and answers with whatever the test queues up.
type fakeRadix struct {
	mu      sync.Mutex
	events  []Notification
	raws    []map[string]any
	tokens  []string
	replies []func(w http.ResponseWriter)
	srv     *httptest.Server
}

func newFakeRadix(t *testing.T) *fakeRadix {
	f := &fakeRadix{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch r.URL.Path {
		case "/v1/link":
			if tok != "hrp_good" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"link_id": "l_x", "label": "Tobias's iPhone", "device_enabled": true})
		case "/v1/events":
			body, _ := io.ReadAll(r.Body)
			var n Notification
			var raw map[string]any
			json.Unmarshal(body, &n)
			json.Unmarshal(body, &raw)
			f.mu.Lock()
			f.events = append(f.events, n)
			f.raws = append(f.raws, raw)
			f.tokens = append(f.tokens, tok)
			var reply func(http.ResponseWriter)
			if len(f.replies) > 0 {
				reply, f.replies = f.replies[0], f.replies[1:]
			}
			f.mu.Unlock()
			if reply != nil {
				reply(w)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"sent": true})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRadix) reply(status int, body string, headers ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies = append(f.replies, func(w http.ResponseWriter) {
		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	})
}

func (f *fakeRadix) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newPusher(t *testing.T, dir string, c *clock) *Pusher {
	t.Helper()
	p, err := Open(dir, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func setup(t *testing.T) (*Pusher, *fakeRadix, *clock, string) {
	t.Helper()
	dir := t.TempDir()
	c := &clock{t: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)}
	f := newFakeRadix(t)
	p := newPusher(t, dir, c)
	if _, err := p.Configure(context.Background(), "p_aaaaaaaa", f.srv.URL, "hrp_good", "", nil); err != nil {
		t.Fatal(err)
	}
	return p, f, c, dir
}

func note(kind string) Notification {
	turn := 3
	return Notification{EventID: "b_x-" + kind, Kind: kind, Title: "fix flaky test", Subtitle: "studio", Body: "body", SessionID: "s_aaaaaaaaaa", Turn: &turn}
}

func codeOf(err error) string {
	var pe *proto.Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

func TestConfigure(t *testing.T) {
	p, f, _, dir := setup(t)
	st := p.Status("p_aaaaaaaa")
	if !st.Configured || st.Label != "Tobias's iPhone" || st.RadixURL != f.srv.URL || !st.Prefs[KindShellExited] {
		t.Fatalf("status %+v", st)
	}
	if p.Status("p_other").Configured {
		t.Fatal("unconfigured key reports configured")
	}
	cases := []struct {
		url, token, code string
		prefs            map[string]bool
	}{
		{f.srv.URL, "hrp_wrong", proto.CodeInvalidArgument, nil},
		{f.srv.URL, "hdt_pairing_token", proto.CodeInvalidArgument, nil},
		{"http://radix.example.com", "hrp_good", proto.CodeInvalidArgument, nil},
		{"ftp://radix.example.com", "hrp_good", proto.CodeInvalidArgument, nil},
		{"https://user:pw@radix.example.com", "hrp_good", proto.CodeInvalidArgument, nil},
		{"http://127.0.0.1:1", "hrp_good", proto.CodeUnavailable, nil},
		{f.srv.URL, "hrp_good", proto.CodeInvalidArgument, map[string]bool{"made_up": true}},
	}
	for _, c := range cases {
		if _, err := p.Configure(context.Background(), "p_bbbbbbbb", c.url, c.token, "", c.prefs); codeOf(err) != c.code {
			t.Errorf("%s %s: %v, want %s", c.url, c.token, err, c.code)
		}
	}
	// The token is stored only in a private file.
	b, _ := os.ReadFile(filepath.Join(dir, "push.json"))
	if !strings.Contains(string(b), "hrp_good") {
		t.Fatal("route not persisted")
	}
	if fi, _ := os.Stat(filepath.Join(dir, "push.json")); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
}

func TestDeliverSuccess(t *testing.T) {
	p, f, _, _ := setup(t)
	if n := p.Enqueue(note(KindClaudeNeedsInput)); n != 1 {
		t.Fatalf("queued for %d routes", n)
	}
	if next := p.Deliver(context.Background()); !next.IsZero() {
		t.Fatalf("queue not empty: next %v", next)
	}
	if f.count() != 1 || f.events[0].Kind != KindClaudeNeedsInput || *f.events[0].Turn != 3 || f.tokens[0] != "hrp_good" {
		t.Fatalf("radix got %+v with %v", f.events, f.tokens)
	}
	if st := p.Status("p_aaaaaaaa"); st.LastSuccessAt == "" || st.Queued != 0 {
		t.Fatalf("status %+v", st)
	}
}

func TestPrefsFilter(t *testing.T) {
	p, f, _, _ := setup(t)
	if _, err := p.Configure(context.Background(), "p_aaaaaaaa", f.srv.URL, "hrp_good", "", map[string]bool{KindClaudeEnded: false}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{KindClaudeEnded, KindClaudeEndedAbnormally} {
		if p.Enqueue(note(k)) != 0 {
			t.Errorf("%s queued despite prefs", k)
		}
	}
	if p.Enqueue(note(KindTest)) != 1 || p.Enqueue(note(KindShellExited)) != 1 {
		t.Fatal("other kinds should still go out")
	}
}

func TestRetryRules(t *testing.T) {
	p, f, c, _ := setup(t)
	ctx := context.Background()

	// 5xx: retried with backoff.
	f.reply(502, `{"error":{"code":"apns_error","message":"APNs answered 503"}}`)
	p.Enqueue(note(KindClaudeFailed))
	next := p.Deliver(ctx)
	if want := c.now().Add(2 * time.Second); !next.Equal(want) {
		t.Fatalf("next %v, want %v", next, want)
	}
	if st := p.Status("p_aaaaaaaa"); st.Queued != 1 || !strings.Contains(st.LastError, "APNs answered 503") {
		t.Fatalf("status %+v", st)
	}
	p.Deliver(ctx) // not due yet
	if f.count() != 1 {
		t.Fatal("retried before it was due")
	}
	c.add(2 * time.Second)
	if next := p.Deliver(ctx); !next.IsZero() || f.count() != 2 {
		t.Fatalf("retry: next %v count %d", next, f.count())
	}
	if st := p.Status("p_aaaaaaaa"); st.LastError != "" {
		t.Fatalf("error not cleared: %+v", st)
	}

	// 429: honours Retry-After.
	f.reply(429, `{"error":{"code":"rate_limited","message":"too many events"}}`, "Retry-After", "30")
	p.Enqueue(note(KindTest))
	if next := p.Deliver(ctx); !next.Equal(c.now().Add(30 * time.Second)) {
		t.Fatalf("429 next %v", next)
	}
	c.add(30 * time.Second)
	p.Deliver(ctx)

	// 400: dropped.
	f.reply(400, `{"error":{"code":"bad_request","message":"unknown kind"}}`)
	p.Enqueue(note(KindTest))
	if next := p.Deliver(ctx); !next.IsZero() {
		t.Fatal("400 should drop the notification")
	}

	// Network failure: retried.
	f.srv.Close()
	p.Enqueue(note(KindTest))
	if next := p.Deliver(ctx); next.IsZero() {
		t.Fatal("network failure should be retried")
	}
}

func TestRevokedTokenDisablesRoute(t *testing.T) {
	p, f, _, _ := setup(t)
	f.reply(401, `{"error":{"code":"unauthorized","message":"missing or invalid credential"}}`)
	p.Enqueue(note(KindTest))
	p.Enqueue(note(KindShellExited))
	p.Deliver(context.Background())
	st := p.Status("p_aaaaaaaa")
	if st.Broken == "" || st.Queued != 0 || f.count() != 1 {
		t.Fatalf("status %+v count %d", st, f.count())
	}
	if p.Enqueue(note(KindTest)) != 0 {
		t.Fatal("broken route still queues")
	}
	// Configuring again repairs it.
	if _, err := p.Configure(context.Background(), "p_aaaaaaaa", f.srv.URL, "hrp_good", "", nil); err != nil {
		t.Fatal(err)
	}
	if p.Status("p_aaaaaaaa").Broken != "" || p.Enqueue(note(KindTest)) != 1 {
		t.Fatal("reconfigure did not repair the route")
	}
}

func TestStaleNotificationsExpire(t *testing.T) {
	p, f, c, _ := setup(t)
	f.reply(502, "")
	p.Enqueue(note(KindTest))
	p.Deliver(context.Background())
	c.add(maxAge + time.Minute)
	if next := p.Deliver(context.Background()); !next.IsZero() || f.count() != 1 {
		t.Fatalf("stale item sent or kept: next %v count %d", next, f.count())
	}
}

func TestQueueAndRoutesSurviveRestart(t *testing.T) {
	p, f, c, dir := setup(t)
	f.reply(502, "")
	p.Enqueue(note(KindClaudeTurnFinished))
	p.Deliver(context.Background())

	p2 := newPusher(t, dir, c)
	if st := p2.Status("p_aaaaaaaa"); !st.Configured || st.Queued != 1 {
		t.Fatalf("after restart %+v", st)
	}
	c.add(time.Minute)
	p2.Deliver(context.Background())
	if f.count() != 2 || f.events[1].Kind != KindClaudeTurnFinished || f.events[1].EventID != f.events[0].EventID {
		t.Fatalf("redelivery %+v", f.events)
	}
}

func TestRemove(t *testing.T) {
	p, f, _, _ := setup(t)
	f.reply(502, "")
	p.Enqueue(note(KindTest))
	p.Deliver(context.Background())
	if !p.Remove("p_aaaaaaaa") || p.Remove("p_aaaaaaaa") {
		t.Fatal("remove result")
	}
	if p.Status("p_aaaaaaaa").Configured || len(p.Routes()) != 0 || p.Enqueue(note(KindTest)) != 0 {
		t.Fatal("route still active")
	}
	p.Deliver(context.Background())
	if f.count() != 1 {
		t.Fatal("removed route's queue was delivered")
	}
}

func TestRunDeliversPromptly(t *testing.T) {
	p, f, _, _ := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	p.Enqueue(note(KindTest))
	deadline := time.Now().Add(5 * time.Second)
	for f.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if f.count() != 1 {
		t.Fatal("Run did not deliver")
	}
}

// --- event mapping ---------------------------------------------------------

func intp(i int) *int       { return &i }
func strp(s string) *string { return &s }
func sess(kind string, exit *proto.Exit) *proto.Session {
	return &proto.Session{ID: "s_aaaaaaaaaa", Kind: kind, Title: "make test", Exit: exit}
}

func TestNotify(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	var lastInput time.Time
	src := Source{BootID: "b_boot", Hostname: "studio", Now: func() time.Time { return now }, LastInput: func(string) time.Time { return lastInput }}
	exited := func(code *int, sig *string, by string) *proto.Exit {
		return &proto.Exit{Code: code, Signal: sig, InitiatedBy: by}
	}
	cases := []struct {
		name  string
		ev    proto.Event
		input time.Time
		kind  string
		body  string
	}{
		{"shell exit", proto.Event{Type: proto.EvSessionExited, Session: sess("shell", exited(intp(2), nil, "process"))}, time.Time{}, KindShellExited, "Exited with code 2"},
		{"shell killed", proto.Event{Type: proto.EvSessionExited, Session: sess("shell", exited(nil, strp("SIGKILL"), "process"))}, time.Time{}, KindShellExited, "Exited: killed by SIGKILL"},
		{"shell closed from app", proto.Event{Type: proto.EvSessionExited, Session: sess("shell", exited(nil, strp("SIGHUP"), "client"))}, time.Time{}, "", ""},
		{"agent shutdown", proto.Event{Type: proto.EvSessionExited, Session: sess("shell", exited(nil, strp("SIGHUP"), "agent"))}, time.Time{}, "", ""},
		{"typed exit", proto.Event{Type: proto.EvSessionExited, Session: sess("shell", exited(intp(0), nil, "process"))}, now.Add(-2 * time.Second), "", ""},
		{"old input", proto.Event{Type: proto.EvSessionExited, Session: sess("shell", exited(intp(0), nil, "process"))}, now.Add(-time.Minute), KindShellExited, "Exited"},
		{"claude session exit is claude.ended's job", proto.Event{Type: proto.EvSessionExited, Session: sess("claude", exited(intp(0), nil, "process"))}, time.Time{}, "", ""},
		{"turn finished", proto.Event{Type: proto.EvClaudeTurnFinished, Session: sess("claude", nil), Turn: intp(4), Preview: strp("All green.")}, time.Time{}, KindClaudeTurnFinished, "All green."},
		{"turn finished, no preview", proto.Event{Type: proto.EvClaudeTurnFinished, Session: sess("claude", nil)}, time.Time{}, KindClaudeTurnFinished, "Claude finished and is waiting for you"},
		{"needs input", proto.Event{Type: proto.EvClaudeNeedsInput, Session: sess("claude", nil), Message: "Claude needs your permission to use Bash"}, now, KindClaudeNeedsInput, "Claude needs your permission to use Bash"},
		{"failed", proto.Event{Type: proto.EvClaudeFailed, Session: sess("claude", nil), Error: "rate_limit", ErrorDetails: strp("429")}, time.Time{}, KindClaudeFailed, "Claude hit an API error: rate_limit (429)"},
		{"claude ended cleanly", proto.Event{Type: proto.EvClaudeEnded, Session: sess("claude", exited(intp(0), nil, "process"))}, time.Time{}, KindClaudeEnded, "Claude Code exited"},
		{"claude crashed", proto.Event{Type: proto.EvClaudeEnded, Session: sess("claude", exited(intp(1), nil, "process"))}, time.Time{}, KindClaudeEndedAbnormally, "Claude Code exited with code 1"},
		{"claude /exit typed", proto.Event{Type: proto.EvClaudeEnded, Session: sess("claude", exited(intp(0), nil, "process"))}, now.Add(-time.Second), "", ""},
		{"claude closed from app", proto.Event{Type: proto.EvClaudeEnded, Session: sess("claude", exited(nil, strp("SIGHUP"), "client"))}, time.Time{}, "", ""},
		{"claude in a shell ended", proto.Event{Type: proto.EvClaudeEnded, Session: sess("shell", nil)}, time.Time{}, KindClaudeEnded, "Claude Code ended"},
		{"other events", proto.Event{Type: proto.EvSessionUpdated, Session: sess("shell", nil)}, time.Time{}, "", ""},
	}
	for _, c := range cases {
		lastInput = c.input
		c.ev.Seq = 42
		n, ok := Notify(c.ev, src)
		if ok != (c.kind != "") || n.Kind != c.kind || (ok && n.Body != c.body) {
			t.Errorf("%s: ok=%v kind=%q body=%q", c.name, ok, n.Kind, n.Body)
			continue
		}
		if ok && (n.EventID != "b_boot-42" || n.Title != "make test" || n.Subtitle != "studio" || n.SessionID != "s_aaaaaaaaaa") {
			t.Errorf("%s: %+v", c.name, n)
		}
	}
}

func TestWatchFollowsBus(t *testing.T) {
	p, f, _, _ := setup(t)
	bus := session.NewBus(100, 2, nil) // tiny queue, so Watch must catch up after lagging
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Watch(ctx, Source{Bus: bus, BootID: "b_boot", Hostname: "studio"}); close(done) }()
	time.Sleep(20 * time.Millisecond) // let Watch subscribe
	for i := 0; i < 10; i++ {
		bus.Publish(proto.Event{Type: proto.EvClaudeNeedsInput, Session: sess("claude", nil), Message: "m"})
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		p.Deliver(context.Background())
		if f.count() == 10 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if f.count() != 10 {
		t.Fatalf("delivered %d of 10", f.count())
	}
	seen := map[string]bool{}
	for _, e := range f.events {
		seen[e.EventID] = true
	}
	if len(seen) != 10 {
		t.Fatalf("duplicate or missing event IDs: %v", seen)
	}
}

func TestEncryptedRoute(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)}
	f := newFakeRadix(t)
	p, err := Open(dir, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: c.now, MachineID: "m_aaaabbbbccccdddd"})
	if err != nil {
		t.Fatal(err)
	}
	keyBytes := make([]byte, 32)
	keyBytes[0] = 7
	key := base64.RawURLEncoding.EncodeToString(keyBytes)
	if _, err := p.Configure(context.Background(), "p_aaaaaaaa", f.srv.URL, "hrp_good", "not-a-key", nil); codeOf(err) != proto.CodeInvalidArgument {
		t.Fatalf("bad key: %v", err)
	}
	if _, err := p.Configure(context.Background(), "p_aaaaaaaa", f.srv.URL, "hrp_good", key, nil); err != nil {
		t.Fatal(err)
	}
	if !p.Status("p_aaaaaaaa").Encrypted {
		t.Fatal("status doesn't say encrypted")
	}
	// The key survives a restart, like the token.
	p = newPusherWithID(t, dir, c, "m_aaaabbbbccccdddd")

	n := note(KindClaudeNeedsInput)
	p.Enqueue(n)
	p.Deliver(context.Background())
	if f.count() != 1 {
		t.Fatalf("%d events", f.count())
	}
	raw := f.raws[0]
	for _, k := range []string{"title", "subtitle", "body"} {
		if _, ok := raw[k]; ok {
			t.Errorf("plaintext %s sent to Radix", k)
		}
	}
	if raw["kind"] != KindClaudeNeedsInput || raw["session_id"] != n.SessionID || raw["turn"] != float64(3) || raw["event_id"] != n.EventID {
		t.Errorf("metadata %v", raw)
	}
	title, subtitle, body, err := Decrypt(keyBytes, raw["encrypted"].(string), "m_aaaabbbbccccdddd", n)
	if err != nil || title != n.Title || subtitle != n.Subtitle || body != n.Body {
		t.Fatalf("decrypted %q %q %q, %v", title, subtitle, body, err)
	}

	// Configuring without a key goes back to plaintext.
	if _, err := p.Configure(context.Background(), "p_aaaaaaaa", f.srv.URL, "hrp_good", "", nil); err != nil {
		t.Fatal(err)
	}
	if p.Status("p_aaaaaaaa").Encrypted {
		t.Fatal("still encrypted")
	}
}

func newPusherWithID(t *testing.T, dir string, c *clock, machineID string) *Pusher {
	t.Helper()
	p, err := Open(dir, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: c.now, MachineID: machineID})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
