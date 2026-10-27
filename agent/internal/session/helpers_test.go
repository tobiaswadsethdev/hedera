package session

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
)

func newTestManager(t *testing.T, mod func(*Config)) *Manager {
	t.Helper()
	home := t.TempDir()
	cfg := Config{
		MachineID:   "m_testtesttesttest",
		Resolve:     func(_ string, argv []string) ([]string, []string) { return argv, argv },
		BaseEnv:     func() []string { return testBaseEnv(home) },
		Home:        t.TempDir(),
		HangupGrace: 300 * time.Millisecond,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if mod != nil {
		mod(&cfg)
	}
	m := NewManager(cfg)
	t.Cleanup(func() {
		for _, s := range m.List() {
			if sess, err := m.Get(s.ID); err == nil && sess.running() {
				sess.proc.Kill()
				<-sess.Exited()
			}
		}
	})
	return m
}

func mustCreate(t *testing.T, m *Manager, argv ...string) *Session {
	t.Helper()
	s, err := m.Create(proto.CreateRequest{Kind: proto.KindShell, Argv: argv})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func waitExit(t *testing.T, s *Session) proto.Session {
	t.Helper()
	select {
	case <-s.Exited():
	case <-time.After(10 * time.Second):
		t.Fatal("session did not exit")
	}
	return s.Snapshot()
}

// collector drains a subscriber in the background.
type collector struct {
	mu     sync.Mutex
	data   []byte
	first  uint64
	chunks int
	end    *End
	gap    error
	done   chan struct{}
}

func collect(sub *Subscriber) *collector {
	c := &collector{done: make(chan struct{}), first: ^uint64(0)}
	go func() {
		defer close(c.done)
		next := ^uint64(0)
		for range sub.Ready() {
			chunks, end := sub.Take()
			c.mu.Lock()
			for _, ch := range chunks {
				if next != ^uint64(0) && ch.Offset != next && c.gap == nil {
					c.gap = errors.New("non-contiguous chunk offsets")
				}
				if c.first == ^uint64(0) {
					c.first = ch.Offset
				}
				next = ch.Offset + uint64(len(ch.Data))
				c.data = append(c.data, ch.Data...)
				c.chunks++
			}
			c.end = end
			c.mu.Unlock()
			if end != nil {
				return
			}
		}
	}()
	return c
}

func (c *collector) waitFor(t *testing.T, substr string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		ok := bytes.Contains(c.data, []byte(substr))
		c.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t.Fatalf("output never contained %q; got %q", substr, c.data)
}

func (c *collector) waitEnd(t *testing.T) *End {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(10 * time.Second):
		t.Fatal("subscription did not end")
	}
	return c.end
}

// testBaseEnv is the environment sessions inherit in tests: what the OS
// needs to start programs, plus variables the agent must strip or keep.
func testBaseEnv(home string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(k) {
		case "PATH", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT", "TEMP", "TMP", "USERPROFILE", "APPDATA", "LOCALAPPDATA":
			env = append(env, kv)
		}
	}
	return append(env, "HOME="+home, "HEDERA_PTY_ID=s_leakedleak", "HEDERA_HOME=/kept", "TERM=dumb",
		"CLAUDECODE=1", "CLAUDE_CODE_SESSION_ID=outer", "CLAUDE_CODE_USE_BEDROCK=1")
}
func mustAttach(t *testing.T, s *Session, from *uint64) *Subscriber {
	t.Helper()
	sub, _, _ := s.Attach(from)
	return sub
}

func eventTypes(sub *EventSub, n int) string {
	var types []string
	for i := 0; i < n; i++ {
		select {
		case ev := <-sub.C:
			types = append(types, ev.Type)
		case <-time.After(5 * time.Second):
			return strings.Join(append(types, "<timeout>"), ",")
		}
	}
	return strings.Join(types, ",")
}
