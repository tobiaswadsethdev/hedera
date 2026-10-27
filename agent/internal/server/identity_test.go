package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"
)

// Trimmed real-shaped output of `tailscale whois --json`.
const whoisUser = `{
  "Node": {"ID": 123, "StableID": "nX", "Name": "iphone.tail1234.ts.net.", "User": 42, "Tags": null},
  "UserProfile": {"ID": 42, "LoginName": "tobias@github", "DisplayName": "Tobias", "ProfilePicURL": ""},
  "CapMap": {}
}`

const whoisTagged = `{
  "Node": {"ID": 124, "Name": "ci-runner.tail1234.ts.net.", "Tags": ["tag:servers"]},
  "UserProfile": {"ID": 1, "LoginName": "tagged-devices", "DisplayName": "Tagged Devices"}
}`

func TestParseWhois(t *testing.T) {
	id, err := parseWhois([]byte(whoisUser))
	if err != nil || id.LoginName != "tobias@github" || id.Node != "iphone.tail1234.ts.net" || len(id.Tags) != 0 {
		t.Fatalf("%+v %v", id, err)
	}
	id, err = parseWhois([]byte(whoisTagged))
	if err != nil || id.LoginName != "" || id.Tags[0] != "tag:servers" {
		t.Fatalf("tagged nodes must not carry the placeholder owner: %+v %v", id, err)
	}
	for _, bad := range []string{"", "{", `{"UserProfile": {"LoginName": "x"}}`} {
		if _, err := parseWhois([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestAllowlist(t *testing.T) {
	a := Allowlist{Users: []string{"Tobias@GitHub"}, Tags: []string{"tag:servers"}}
	cases := []struct {
		id   Identity
		want bool
	}{
		{Identity{LoginName: "tobias@github"}, true},
		{Identity{LoginName: "mallory@github"}, false},
		{Identity{Tags: []string{"tag:servers"}}, true},
		{Identity{Tags: []string{"tag:other"}}, false},
		{Identity{}, false},
	}
	for _, c := range cases {
		if got := a.Admits(c.id); got != c.want {
			t.Errorf("%+v: %v", c.id, got)
		}
	}
	if !(Allowlist{}).Empty() || a.Empty() {
		t.Fatal("Empty")
	}
}

func TestWhoisCache(t *testing.T) {
	var mu sync.Mutex
	calls, fail := 0, false
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	w := func(context.Context, netip.Addr) (Identity, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if fail {
			return Identity{}, errors.New("tailscaled not running")
		}
		return Identity{LoginName: "tobias@github"}, nil
	}
	c := newCachedWhois(w, time.Minute, func() time.Time { return now })
	ip := netip.MustParseAddr("100.64.0.5")
	c.lookup(context.Background(), ip)
	c.lookup(context.Background(), ip)
	if calls != 1 {
		t.Fatalf("%d calls", calls)
	}
	now = now.Add(2 * time.Minute)
	fail = true
	if _, err := c.lookup(context.Background(), ip); err == nil {
		t.Fatal("expired entry served")
	}
	fail = false
	if id, err := c.lookup(context.Background(), ip); err != nil || id.LoginName != "tobias@github" || calls != 3 {
		t.Fatalf("errors must not be cached: %v calls=%d", err, calls)
	}
}

func TestIdentityCheck(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	ids := map[string]Identity{
		"100.64.0.1": {LoginName: "tobias@github"},
		"100.64.0.2": {LoginName: "guest@github"},
	}
	s := &Server{
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Allow: Allowlist{Users: []string{"tobias@github"}},
		Whois: func(_ context.Context, a netip.Addr) (Identity, error) {
			if id, ok := ids[a.String()]; ok {
				return id, nil
			}
			return Identity{}, errors.New("no such peer")
		},
	}
	h := s.identityCheck(ok)
	cases := map[string]int{
		"100.64.0.1:5000": http.StatusNoContent,
		"100.64.0.2:5000": http.StatusForbidden,
		"100.64.0.3:5000": http.StatusForbidden, // whois failed: fail closed
		"127.0.0.1:5000":  http.StatusForbidden, // loopback only passes in dev mode
	}
	for peer, want := range cases {
		req := httptest.NewRequest("GET", "/v1/ws", nil)
		req.RemoteAddr = peer
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("%s: %d, want %d", peer, rec.Code, want)
		}
	}
	s.AllowLoopbackPeers = true
	req := httptest.NewRequest("GET", "/v1/ws", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("dev loopback: %d", rec.Code)
	}
}

func TestRemoteHandlerUsesAllowlist(t *testing.T) {
	s := &Server{
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Allow: Allowlist{Users: []string{"tobias@github"}},
		Whois: func(context.Context, netip.Addr) (Identity, error) { return Identity{LoginName: "guest@github"}, nil },
	}
	req := httptest.NewRequest("POST", "/v1/pair", nil)
	req.RemoteAddr = "100.64.0.9:1"
	rec := httptest.NewRecorder()
	s.RemoteHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("pairing from a non-allowlisted peer: %d", rec.Code)
	}
}
