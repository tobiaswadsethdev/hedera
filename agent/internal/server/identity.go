package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Identity is who Tailscale says a peer is.
type Identity struct {
	LoginName string   // the node owner's login, e.g. "tobias@github"
	Node      string   // the node's MagicDNS name
	Tags      []string // ACL tags, for tagged (unowned) nodes
}

// Whois looks up the tailnet identity behind an address.
type Whois func(ctx context.Context, addr netip.Addr) (Identity, error)

// Allowlist restricts the remote listener to some tailnet identities. An
// empty allowlist admits every tailnet peer (a token is still required).
type Allowlist struct {
	Users []string // login names, matched case-insensitively
	Tags  []string // e.g. "tag:servers"
}

func (a Allowlist) Empty() bool { return len(a.Users) == 0 && len(a.Tags) == 0 }

func (a Allowlist) Admits(id Identity) bool {
	for _, u := range a.Users {
		if id.LoginName != "" && strings.EqualFold(u, id.LoginName) {
			return true
		}
	}
	for _, want := range a.Tags {
		for _, have := range id.Tags {
			if want == have {
				return true
			}
		}
	}
	return false
}

// whoisResponse is the part of `tailscale whois --json` the agent reads
// (apitype.WhoIsResponse, which has no JSON tags).
type whoisResponse struct {
	Node *struct {
		Name string
		Tags []string
	}
	UserProfile *struct {
		LoginName string
	}
}

func parseWhois(out []byte) (Identity, error) {
	var r whoisResponse
	if err := json.Unmarshal(out, &r); err != nil {
		return Identity{}, fmt.Errorf("parsing tailscale whois: %w", err)
	}
	if r.Node == nil {
		return Identity{}, errors.New("tailscale whois: no node for this address")
	}
	id := Identity{Node: strings.TrimSuffix(r.Node.Name, "."), Tags: r.Node.Tags}
	// Tagged nodes report a placeholder owner; only trust the login of
	// untagged ones.
	if r.UserProfile != nil && len(r.Node.Tags) == 0 {
		id.LoginName = r.UserProfile.LoginName
	}
	return id, nil
}

// TailscaleCLI finds the tailscale command line tool, including the copies
// bundled with the macOS and Windows apps.
func TailscaleCLI() (string, error) {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p, nil
	}
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{"/Applications/Tailscale.app/Contents/MacOS/Tailscale"}
	case "windows":
		candidates = []string{`C:\Program Files\Tailscale\tailscale.exe`}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", errors.New("the tailscale command line tool was not found")
}

// CLIWhois asks `tailscale whois --json` about an address.
func CLIWhois(bin string) Whois {
	return func(ctx context.Context, addr netip.Addr) (Identity, error) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, bin, "whois", "--json", addr.String()).Output()
		if err != nil {
			return Identity{}, fmt.Errorf("tailscale whois %s: %w", addr, err)
		}
		return parseWhois(out)
	}
}

// cachedWhois remembers answers for a minute, since every request on the
// remote listener needs one.
type cachedWhois struct {
	whois Whois
	ttl   time.Duration
	now   func() time.Time

	mu      sync.Mutex
	entries map[netip.Addr]whoisEntry
}

type whoisEntry struct {
	id Identity
	at time.Time
}

func newCachedWhois(w Whois, ttl time.Duration, now func() time.Time) *cachedWhois {
	return &cachedWhois{whois: w, ttl: ttl, now: now, entries: map[netip.Addr]whoisEntry{}}
}

func (c *cachedWhois) lookup(ctx context.Context, addr netip.Addr) (Identity, error) {
	c.mu.Lock()
	e, ok := c.entries[addr]
	c.mu.Unlock()
	if ok && c.now().Sub(e.at) < c.ttl {
		return e.id, nil
	}
	id, err := c.whois(ctx, addr)
	if err != nil {
		return id, err // not cached: a transient failure shouldn't lock a peer out
	}
	c.mu.Lock()
	c.entries[addr] = whoisEntry{id: id, at: c.now()}
	c.mu.Unlock()
	return id, nil
}
