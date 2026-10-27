package pairing

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

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

func newStore(t *testing.T) (*Store, *clock, string) {
	t.Helper()
	c := &clock{t: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "pairings.json")
	s, err := Open(path, c.now)
	if err != nil {
		t.Fatal(err)
	}
	return s, c, path
}

func TestRedeemAndAuthenticate(t *testing.T) {
	s, _, path := newStore(t)
	_, code, _ := s.NewCode()
	p, token, err := s.Redeem(code, "  Tobias's iPhone ")
	if err != nil {
		t.Fatal(err)
	}
	if p.DeviceName != "Tobias's iPhone" || !strings.HasPrefix(token, "hdt_") {
		t.Fatalf("pairing %+v token %q", p, token)
	}
	if id, ok := s.Authenticate(token); !ok || id != p.ID {
		t.Fatalf("authenticate: %q %v", id, ok)
	}
	if _, ok := s.Authenticate(token + "x"); ok {
		t.Fatal("wrong token accepted")
	}
	if _, ok := s.Authenticate(""); ok {
		t.Fatal("empty token accepted")
	}

	// The file holds hashes only, with owner-only permissions.
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), token) || strings.Contains(string(raw), code) {
		t.Fatal("secret written to disk")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}

	// A fresh store loaded from disk still accepts the token.
	s2, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Authenticate(token); !ok {
		t.Fatal("token not persisted")
	}
}

func TestCodeIsSingleUse(t *testing.T) {
	s, _, _ := newStore(t)
	_, code, _ := s.NewCode()
	if _, _, err := s.Redeem(code, "a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Redeem(code, "b"); err != ErrBadCode {
		t.Fatalf("second redeem: %v", err)
	}
	if _, _, err := s.Redeem("nonsense", "c"); err != ErrBadCode {
		t.Fatalf("unknown code: %v", err)
	}
}

func TestCodeExpires(t *testing.T) {
	s, c, _ := newStore(t)
	id, code, _ := s.NewCode()
	c.add(CodeTTL + time.Second)
	if _, _, err := s.Redeem(code, "a"); err != ErrBadCode {
		t.Fatalf("expired redeem: %v", err)
	}
	st, err := s.WaitCode(context.Background(), id)
	if err != nil || st.Status != "expired" {
		t.Fatalf("status %+v %v", st, err)
	}
}

func TestWaitCodeCompletes(t *testing.T) {
	s, _, _ := newStore(t)
	id, code, _ := s.NewCode()
	got := make(chan CodeStatus)
	go func() {
		st, _ := s.WaitCode(context.Background(), id)
		got <- st
	}()
	time.Sleep(20 * time.Millisecond)
	p, _, err := s.Redeem(code, "phone")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case st := <-got:
		if st.Status != "completed" || st.PairingID != p.ID || st.DeviceName != "phone" {
			t.Fatalf("status %+v", st)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitCode did not return")
	}
	if _, err := s.WaitCode(context.Background(), "c_missing"); err != ErrNotFound {
		t.Fatalf("missing code: %v", err)
	}
}

func TestWaitCodeHonorsContext(t *testing.T) {
	s, _, _ := newStore(t)
	id, _, _ := s.NewCode()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	st, err := s.WaitCode(ctx, id)
	if err != nil || st.Status != "pending" {
		t.Fatalf("status %+v %v", st, err)
	}
}

func TestRevoke(t *testing.T) {
	s, _, _ := newStore(t)
	_, code, _ := s.NewCode()
	p, token, _ := s.Redeem(code, "phone")
	_, code2, _ := s.NewCode()
	p2, token2, _ := s.Redeem(code2, "ipad")
	if err := s.Revoke(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Authenticate(token); ok {
		t.Fatal("revoked token accepted")
	}
	if id, ok := s.Authenticate(token2); !ok || id != p2.ID {
		t.Fatal("other pairing affected by revoke")
	}
	if err := s.Revoke(p.ID); err != ErrNotFound {
		t.Fatalf("double revoke: %v", err)
	}
	for _, l := range s.List() {
		if l.TokenHash != "" {
			t.Fatal("List exposes token hashes")
		}
	}
}

func TestDeviceNameLimits(t *testing.T) {
	s, _, _ := newStore(t)
	_, code, _ := s.NewCode()
	p, _, _ := s.Redeem(code, strings.Repeat("x", 500))
	if len(p.DeviceName) != maxDeviceName {
		t.Fatalf("len %d", len(p.DeviceName))
	}
	_, code, _ = s.NewCode()
	if p, _, _ = s.Redeem(code, "   "); p.DeviceName != "unnamed device" {
		t.Fatalf("name %q", p.DeviceName)
	}
}
