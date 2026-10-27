//go:build !windows

package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
)

// resumeManager runs sessions through a stand-in for `claude` that prints
// its arguments, so resuming never starts the real thing.
func resumeManager(t *testing.T, path string) *Manager {
	t.Helper()
	return newTestManager(t, func(c *Config) {
		c.ResumePath = path
		c.Resolve = func(_ string, argv []string) ([]string, []string) {
			return argv, append([]string{"/bin/sh", "-c", `echo "args:$*"; read x`, "sh"}, argv...)
		}
	})
}

func startClaude(t *testing.T, m *Manager, cwd, claudeID string) *Session {
	t.Helper()
	s, err := m.Create(proto.CreateRequest{Kind: proto.KindClaude, Cwd: cwd, Title: "fix flaky test"})
	if err != nil {
		t.Fatal(err)
	}
	s.ApplyHook(HookEvent{HookEventName: HookSessionStart, SessionID: claudeID, Source: "startup"})
	s.ApplyHook(HookEvent{HookEventName: HookUserPromptSubmit, SessionID: claudeID})
	return s
}

func shutdown(m *Manager) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	m.Shutdown(ctx)
}

func TestResumeAfterAgentRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-sessions.json")
	cwd := t.TempDir()
	m1 := resumeManager(t, path)
	startClaude(t, m1, cwd, "abc-123")
	if len(m1.Resumable()) != 0 {
		t.Fatal("a live session is not resumable")
	}
	shutdown(m1) // the agent stops: sessions die, the record stays

	m2 := resumeManager(t, path)
	list := m2.Resumable()
	if len(list) != 1 || list[0].ClaudeSessionID != "abc-123" || list[0].Cwd != cwd || list[0].Title != "fix flaky test" {
		t.Fatalf("resumable %+v", list)
	}
	s, err := m2.Resume("abc-123", 100, 30)
	if err != nil {
		t.Fatal(err)
	}
	snap := s.Snapshot()
	if snap.Kind != proto.KindClaude || snap.Cwd != cwd || snap.Title != "fix flaky test" || snap.Size.Cols != 100 {
		t.Fatalf("resumed %+v", snap)
	}
	if strings.Join(snap.Argv, " ") != "claude --resume abc-123" {
		t.Fatalf("argv %q", snap.Argv)
	}
	c := collect(mustAttach(t, s, nil))
	c.waitFor(t, "args:claude --resume abc-123")
	if len(m2.Resumable()) != 0 {
		t.Fatal("still listed after resuming")
	}
	var pe *proto.Error
	if _, err := m2.Resume("abc-123", 0, 0); !errors.As(err, &pe) || pe.Code != proto.CodeNotFound {
		t.Fatalf("second resume: %v", err)
	}
}

func TestCleanEndsAreNotResumable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-sessions.json")
	m1 := resumeManager(t, path)

	// Claude exits by itself.
	a := startClaude(t, m1, t.TempDir(), "exited")
	a.Write([]byte("\r"))
	waitExit(t, a)
	// Closed from a client.
	b := startClaude(t, m1, t.TempDir(), "closed")
	b.Close("kill")
	waitExit(t, b)
	// Claude ended inside a shell that lives on.
	sh, _ := m1.Create(proto.CreateRequest{Kind: proto.KindShell, Argv: []string{"/bin/sh", "-c", "read x"}})
	sh.ApplyHook(HookEvent{HookEventName: HookSessionStart, SessionID: "in-shell", Source: "startup"})
	sh.ApplyHook(HookEvent{HookEventName: HookSessionEnd, SessionID: "in-shell", Reason: "prompt_input_exit"})
	// Live when the agent stops: the only one that should come back.
	startClaude(t, m1, t.TempDir(), "survivor")
	shutdown(m1)

	m2 := resumeManager(t, path)
	var ids []string
	for _, r := range m2.Resumable() {
		ids = append(ids, r.ClaudeSessionID)
	}
	if strings.Join(ids, ",") != "survivor" {
		t.Fatalf("resumable %v", ids)
	}
}

func TestClaudeInShellIsResumableAsClaudeSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-sessions.json")
	m1 := resumeManager(t, path)
	sh, _ := m1.Create(proto.CreateRequest{Kind: proto.KindShell, Argv: []string{"/bin/sh", "-c", "read x"}, Title: "my shell"})
	sh.ApplyHook(HookEvent{HookEventName: HookSessionStart, SessionID: "from-shell", Source: "startup"})
	shutdown(m1)
	m2 := resumeManager(t, path)
	s, err := m2.Resume("from-shell", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if snap := s.Snapshot(); snap.Kind != proto.KindClaude || snap.Argv[0] != "claude" || snap.Title != "my shell" {
		t.Fatalf("resumed %+v", snap)
	}
}

func TestForgetAndMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-sessions.json")
	gone := filepath.Join(t.TempDir(), "project")
	os.Mkdir(gone, 0o700)
	m1 := resumeManager(t, path)
	startClaude(t, m1, gone, "dir-gone")
	startClaude(t, m1, t.TempDir(), "forget-me")
	shutdown(m1)
	os.Remove(gone)

	m2 := resumeManager(t, path)
	var pe *proto.Error
	if _, err := m2.Resume("dir-gone", 0, 0); !errors.As(err, &pe) || pe.Code != proto.CodeInvalidArgument {
		t.Fatalf("missing cwd: %v", err)
	}
	if len(m2.Resumable()) != 2 {
		t.Fatal("a failed resume must keep the entry")
	}
	if err := m2.Forget("forget-me"); err != nil {
		t.Fatal(err)
	}
	if err := m2.Forget("forget-me"); !errors.As(err, &pe) || pe.Code != proto.CodeNotFound {
		t.Fatalf("double forget: %v", err)
	}
	m3 := resumeManager(t, path)
	if l := m3.Resumable(); len(l) != 1 || l[0].ClaudeSessionID != "dir-gone" {
		t.Fatalf("forget not persisted: %+v", l)
	}
}

func TestResumeStoreExpiresAndValidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-sessions.json")
	old := time.Now().Add(-8 * 24 * time.Hour).UTC().Format(time.RFC3339)
	recent := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	os.WriteFile(path, []byte(`[
	  {"pty_id":"s_aaaaaaaaaa","claude_session_id":"old","cwd":"/","title":"t","claude":"claude","updated_at":"`+old+`"},
	  {"pty_id":"s_bbbbbbbbbb","claude_session_id":"$(rm -rf ~)","cwd":"/","title":"t","claude":"claude","updated_at":"`+recent+`"},
	  {"pty_id":"s_cccccccccc","claude_session_id":"fine-1","cwd":"/","title":"t","claude":"claude","updated_at":"`+recent+`"}
	]`), 0o600)
	m := resumeManager(t, path)
	if l := m.Resumable(); len(l) != 1 || l[0].ClaudeSessionID != "fine-1" {
		t.Fatalf("resumable %+v", l)
	}
	// A corrupt file doesn't stop the agent.
	os.WriteFile(path, []byte("{"), 0o600)
	if l := resumeManager(t, path).Resumable(); len(l) != 0 {
		t.Fatalf("corrupt file: %+v", l)
	}
}
