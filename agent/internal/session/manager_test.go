//go:build !windows

package session

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
)

func TestCreateRunsAndReportsExit(t *testing.T) {
	m := newTestManager(t, nil)
	sub, _, _, _ := m.Bus().Subscribe(nil)
	s := mustCreate(t, m, "/bin/sh", "-c", "read x; echo hello; exit 3")
	live := collect(mustAttach(t, s, nil))
	s.Write([]byte("\r"))
	snap := waitExit(t, s)
	if end := live.waitEnd(t); end.Reason != EndExited {
		t.Fatalf("live subscriber end %+v", end)
	}
	if snap.Status != proto.StatusExited || snap.Exit == nil || snap.Exit.Code == nil || *snap.Exit.Code != 3 {
		t.Fatalf("exit = %+v", snap.Exit)
	}
	if snap.Exit.InitiatedBy != "process" || snap.Exit.Signal != nil || snap.Attached != 0 {
		t.Fatalf("exit = %+v", snap.Exit)
	}
	c := collect(mustAttach(t, s, nil))
	c.waitEnd(t)
	if !bytes.Contains(c.data, []byte("hello")) {
		t.Fatalf("output %q", c.data)
	}
	if got := eventTypes(sub, 3); got != "session.created,session.updated,session.exited" {
		t.Fatalf("events %s", got)
	}
}

func TestEnvironment(t *testing.T) {
	m := newTestManager(t, func(c *Config) { c.HookURL = "http://127.0.0.1:1/v1/hook" })
	s, err := m.Create(proto.CreateRequest{
		Kind: proto.KindShell,
		Argv: []string{"/bin/sh", "-c", `echo "id=$HEDERA_PTY_ID m=$HEDERA_MACHINE_ID term=$TERM foo=$FOO home=$HEDERA_HOME url=$HEDERA_HOOK_URL cc=${CLAUDECODE:-unset} ccs=${CLAUDE_CODE_SESSION_ID:-unset} bedrock=$CLAUDE_CODE_USE_BEDROCK tok=${#HEDERA_HOOK_TOKEN}"`},
		Env:  map[string]string{"FOO": "bar"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := collect(mustAttach(t, s, nil))
	want := "id=" + s.ID() + " m=m_testtesttesttest term=xterm-256color foo=bar home=/kept url=http://127.0.0.1:1/v1/hook cc=unset ccs=unset bedrock=1 tok="
	c.waitFor(t, want)
	if len(s.HookToken()) < 40 {
		t.Fatalf("hook token too short: %d", len(s.HookToken()))
	}
}

func TestCreateValidation(t *testing.T) {
	m := newTestManager(t, nil)
	cases := map[string]proto.CreateRequest{
		"bad kind":       {Kind: "vim"},
		"reserved env":   {Kind: proto.KindShell, Env: map[string]string{"HEDERA_PTY_ID": "x"}},
		"bad env name":   {Kind: proto.KindShell, Env: map[string]string{"A=B": "x"}},
		"relative cwd":   {Kind: proto.KindShell, Cwd: "src"},
		"missing cwd":    {Kind: proto.KindShell, Cwd: "/does/not/exist"},
		"size too big":   {Kind: proto.KindShell, Cols: 5000, Rows: 10},
		"missing binary": {Kind: proto.KindShell, Argv: []string{"/no/such/binary"}},
	}
	for name, req := range cases {
		_, err := m.Create(req)
		var pe *proto.Error
		if !errors.As(err, &pe) || pe.Code != proto.CodeInvalidArgument {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestCwdTilde(t *testing.T) {
	m := newTestManager(t, nil)
	if err := os.Mkdir(m.cfg.Home+"/proj", 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := m.Create(proto.CreateRequest{Kind: proto.KindShell, Cwd: "~/proj", Argv: []string{"/bin/sh", "-c", "pwd"}})
	if err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().Cwd != m.cfg.Home+"/proj" {
		t.Fatalf("cwd %s", s.Snapshot().Cwd)
	}
	if s.Snapshot().Title != "sh — proj" {
		t.Fatalf("title %q", s.Snapshot().Title)
	}
	c := collect(mustAttach(t, s, nil))
	c.waitFor(t, "/proj")
}

func TestInputAndSubmit(t *testing.T) {
	m := newTestManager(t, nil)
	s := mustCreate(t, m, "/bin/sh", "-c", `read a; echo "got-a:$a"; read b; echo "got-b:$b"`)
	c := collect(mustAttach(t, s, nil))
	if err := s.Write([]byte("first\r")); err != nil {
		t.Fatal(err)
	}
	c.waitFor(t, "got-a:first")
	if err := s.Input("second", true, nil); err != nil {
		t.Fatal(err)
	}
	c.waitFor(t, "got-b:second")
	waitExit(t, s)
	var pe *proto.Error
	if err := s.Write([]byte("x")); !errors.As(err, &pe) || pe.Code != proto.CodeSessionExited {
		t.Fatalf("write after exit: %v", err)
	}
}

func TestResize(t *testing.T) {
	m := newTestManager(t, nil)
	sub, _, _, _ := m.Bus().Subscribe(nil)
	s := mustCreate(t, m, "/bin/sh", "-c", "read x; stty size")
	c := collect(mustAttach(t, s, nil))
	if err := s.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	s.Write([]byte("\r"))
	c.waitFor(t, "30 100")
	if s.Snapshot().Size != (proto.Size{Cols: 100, Rows: 30}) {
		t.Fatalf("size %+v", s.Snapshot().Size)
	}
	if got := eventTypes(sub, 4); !strings.Contains(got, "session.resized") {
		t.Fatalf("events %s", got)
	}
	var pe *proto.Error
	if err := s.Resize(0, 10); !errors.As(err, &pe) || pe.Code != proto.CodeInvalidArgument {
		t.Fatalf("bad resize: %v", err)
	}
}

func TestCloseHangup(t *testing.T) {
	m := newTestManager(t, nil)
	s := mustCreate(t, m, "/bin/sh", "-c", "sleep 100")
	if err := s.Close("hangup"); err != nil {
		t.Fatal(err)
	}
	snap := waitExit(t, s)
	if snap.Exit.Signal == nil || *snap.Exit.Signal != "SIGHUP" || snap.Exit.InitiatedBy != "client" {
		t.Fatalf("exit %+v", snap.Exit)
	}
	var pe *proto.Error
	if err := s.Close("hangup"); !errors.As(err, &pe) || pe.Code != proto.CodeSessionExited {
		t.Fatalf("second close: %v", err)
	}
}

func TestCloseEscalatesToKill(t *testing.T) {
	m := newTestManager(t, nil)
	s := mustCreate(t, m, "/bin/sh", "-c", `trap "" HUP; echo ready; sleep 100`)
	c := collect(mustAttach(t, s, nil))
	c.waitFor(t, "ready")
	start := time.Now()
	if err := s.Close(""); err != nil {
		t.Fatal(err)
	}
	snap := waitExit(t, s)
	if snap.Exit.Signal == nil || *snap.Exit.Signal != "SIGKILL" {
		t.Fatalf("exit %+v", snap.Exit)
	}
	if d := time.Since(start); d < 250*time.Millisecond {
		t.Fatalf("killed after %v, before the grace period", d)
	}
}

func TestCloseKill(t *testing.T) {
	m := newTestManager(t, nil)
	s := mustCreate(t, m, "/bin/sh", "-c", `trap "" HUP; sleep 100`)
	if err := s.Close("kill"); err != nil {
		t.Fatal(err)
	}
	if snap := waitExit(t, s); *snap.Exit.Signal != "SIGKILL" {
		t.Fatalf("exit %+v", snap.Exit)
	}
	var pe *proto.Error
	if err := s.Close("nuke"); !errors.As(err, &pe) || pe.Code != proto.CodeInvalidArgument {
		t.Fatalf("bad mode: %v", err)
	}
}

func TestAttachReplayOffsets(t *testing.T) {
	m := newTestManager(t, nil)
	s := mustCreate(t, m, "/bin/sh", "-c", "printf 0123456789")
	waitExit(t, s)
	head := s.Snapshot().OutputOffset
	if head != 10 {
		t.Fatalf("output_offset %d", head)
	}

	sub, replay, _ := s.Attach(nil)
	c := collect(sub)
	if end := c.waitEnd(t); end.Reason != EndExited {
		t.Fatalf("end %+v", end)
	}
	if replay != (proto.Replay{From: 0, To: 10}) || string(c.data) != "0123456789" {
		t.Fatalf("replay %+v data %q", replay, c.data)
	}

	five := uint64(5)
	sub, replay, _ = s.Attach(&five)
	c = collect(sub)
	c.waitEnd(t)
	if replay != (proto.Replay{From: 5, To: 10}) || string(c.data) != "56789" {
		t.Fatalf("replay %+v data %q", replay, c.data)
	}

	past := uint64(99)
	_, replay, _ = s.Attach(&past)
	if !replay.Truncated || replay.From != 0 {
		t.Fatalf("past-end replay %+v", replay)
	}
}

func TestReplayTruncatedAlignsUTF8(t *testing.T) {
	m := newTestManager(t, func(c *Config) { c.ScrollbackBytes = 8 })
	// "✓" is 3 bytes; 4 of them are 12 bytes, so an 8-byte buffer starts
	// mid-character.
	s := mustCreate(t, m, "/bin/sh", "-c", `printf '\342\234\223\342\234\223\342\234\223\342\234\223'`)
	waitExit(t, s)
	zero := uint64(0)
	sub, replay, _ := s.Attach(&zero)
	c := collect(sub)
	c.waitEnd(t)
	if !replay.Truncated || replay.From != 6 || replay.To != 12 {
		t.Fatalf("replay %+v", replay)
	}
	if string(c.data) != "✓✓" {
		t.Fatalf("data %q", c.data)
	}
}

// TestAttachMidStreamHasNoGap attaches while output is flowing and checks that
// replay plus live output is exactly the stream from replay.From.
func TestAttachMidStreamHasNoGap(t *testing.T) {
	m := newTestManager(t, nil)
	s := mustCreate(t, m, "/bin/sh", "-c", `sleep 0.1; i=0; while [ $i -lt 3000 ]; do echo "line $i"; i=$((i+1)); done`)
	time.Sleep(150 * time.Millisecond)
	sub, replay, _ := s.Attach(nil)
	c := collect(sub)
	c.waitEnd(t)
	if c.gap != nil {
		t.Fatal(c.gap)
	}
	s.mu.Lock()
	want := s.ring.ReadFrom(replay.From)
	s.mu.Unlock()
	if !bytes.Equal(c.data, want) {
		t.Fatalf("collected %d bytes, stream has %d", len(c.data), len(want))
	}
	if !bytes.Contains(c.data, []byte("line 2999")) {
		t.Fatal("missing tail of output")
	}
}

func TestSlowSubscriberLags(t *testing.T) {
	m := newTestManager(t, func(c *Config) { c.SubscriberLimit = 4096 })
	s := mustCreate(t, m, "/bin/sh", "-c", `i=0; while [ $i -lt 2000 ]; do echo "line $i"; i=$((i+1)); done; sleep 0.2`)
	sub := mustAttach(t, s, nil) // never drained while output flows
	waitExit(t, s)
	chunks, end := sub.Take()
	if end == nil || end.Reason != EndLagged {
		t.Fatalf("end %+v", end)
	}
	var next uint64
	if len(chunks) > 0 {
		last := chunks[len(chunks)-1]
		next = last.Offset + uint64(len(last.Data))
	}
	if end.Offset != next {
		t.Fatalf("lag offset %d, but delivered up to %d", end.Offset, next)
	}
	if s.Snapshot().Attached != 0 {
		t.Fatal("lagged subscriber still counted as attached")
	}
	// Resuming from the lag offset continues the stream exactly.
	sub2, replay, _ := s.Attach(&end.Offset)
	if replay.From != end.Offset || replay.Truncated {
		t.Fatalf("resume replay %+v", replay)
	}
	c := collect(sub2)
	c.waitEnd(t)
	if !bytes.Contains(c.data, []byte("line 1999")) {
		t.Fatal("resume did not reach the end of output")
	}
}

func TestDetach(t *testing.T) {
	m := newTestManager(t, nil)
	s := mustCreate(t, m, "/bin/sh", "-c", "sleep 100")
	sub := mustAttach(t, s, nil)
	if s.Snapshot().Attached != 1 {
		t.Fatal("not attached")
	}
	s.Detach(sub)
	s.Detach(sub) // idempotent
	if s.Snapshot().Attached != 0 {
		t.Fatal("still attached")
	}
	if _, end := sub.Take(); end == nil || end.Reason != EndDetached {
		t.Fatalf("end %+v", end)
	}
}

func TestRemove(t *testing.T) {
	m := newTestManager(t, nil)
	s := mustCreate(t, m, "/bin/sh", "-c", "read x")
	var pe *proto.Error
	if err := m.Remove(s.ID()); !errors.As(err, &pe) || pe.Code != proto.CodeSessionRunning {
		t.Fatalf("remove running: %v", err)
	}
	s.Write([]byte("\r"))
	waitExit(t, s)
	evs, _, _, _ := m.Bus().Subscribe(nil)
	if err := m.Remove(s.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get(s.ID()); !errors.As(err, &pe) || pe.Code != proto.CodeNotFound {
		t.Fatalf("get removed: %v", err)
	}
	if got := eventTypes(evs, 1); got != "session.removed" {
		t.Fatalf("events %s", got)
	}
}

func TestRename(t *testing.T) {
	m := newTestManager(t, nil)
	s := mustCreate(t, m, "/bin/sh", "-c", "read x")
	evs, _, _, _ := m.Bus().Subscribe(nil)
	if err := s.Rename("  fix flaky test \n"); err != nil {
		t.Fatal(err)
	}
	ev := <-evs.C
	if ev.Type != proto.EvSessionUpdated || ev.Reason != "renamed" || ev.Session.Title != "fix flaky test" {
		t.Fatalf("event %+v", ev)
	}
	if got := s.Snapshot().Title; got != "fix flaky test" {
		t.Fatalf("title %q", got)
	}
	var pe *proto.Error
	for _, bad := range []string{" ", strings.Repeat("é", 201)} {
		if err := s.Rename(bad); !errors.As(err, &pe) || pe.Code != proto.CodeInvalidArgument {
			t.Fatalf("rename %q: %v", bad, err)
		}
	}
	if err := s.Rename(strings.Repeat("é", 200)); err != nil {
		t.Fatal(err)
	}
}

func TestMaxSessions(t *testing.T) {
	m := newTestManager(t, func(c *Config) { c.MaxSessions = 1 })
	s := mustCreate(t, m, "/bin/sh", "-c", "read x")
	_, err := m.Create(proto.CreateRequest{Kind: proto.KindShell, Argv: []string{"true"}})
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != proto.CodeLimitExceeded {
		t.Fatalf("over limit: %v", err)
	}
	s.Write([]byte("\r"))
	waitExit(t, s)
	// Exited sessions don't count against the running limit.
	waitExit(t, mustCreate(t, m, "true"))
}

func TestSweepEvictsExited(t *testing.T) {
	var mu sync.Mutex
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	m := newTestManager(t, func(c *Config) { c.MaxExited = 2; c.ExitedTTL = time.Hour; c.Now = clock })
	var all []*Session
	for i := 0; i < 3; i++ {
		s := mustCreate(t, m, "true")
		waitExit(t, s)
		all = append(all, s)
		mu.Lock()
		now = now.Add(time.Minute)
		mu.Unlock()
	}
	if _, err := m.Get(all[0].ID()); err == nil {
		t.Fatal("oldest exited session should be evicted beyond MaxExited")
	}
	if len(m.List()) != 2 {
		t.Fatalf("have %d sessions", len(m.List()))
	}
	mu.Lock()
	now = now.Add(2 * time.Hour)
	mu.Unlock()
	m.Sweep()
	if len(m.List()) != 0 {
		t.Fatalf("TTL sweep left %d sessions", len(m.List()))
	}
}

func TestClaudeKindStartsInStarting(t *testing.T) {
	m := newTestManager(t, nil)
	s, err := m.Create(proto.CreateRequest{Kind: proto.KindClaude, Argv: []string{"/bin/sh", "-c", `read x; echo "x=$x"`}})
	if err != nil {
		t.Fatal(err)
	}
	snap := s.Snapshot()
	if snap.Status != proto.StatusStarting || snap.Claude == nil {
		t.Fatalf("snapshot %+v", snap)
	}
	c := collect(mustAttach(t, s, nil))
	stale, current := 3, 0
	var pe *proto.Error
	if err := s.Input("nope", true, &stale); !errors.As(err, &pe) || pe.Code != proto.CodeStale {
		t.Fatalf("stale input: %v", err)
	}
	if err := s.Input("yes", true, &current); err != nil {
		t.Fatal(err)
	}
	c.waitFor(t, "x=yes")
}

func TestIfTurnRejectedForShell(t *testing.T) {
	m := newTestManager(t, nil)
	s := mustCreate(t, m, "/bin/sh", "-c", "read x")
	turn := 0
	var pe *proto.Error
	if err := s.Input("x", false, &turn); !errors.As(err, &pe) || pe.Code != proto.CodeInvalidArgument {
		t.Fatalf("if_turn on shell: %v", err)
	}
}

func TestShutdownHangsUpEverything(t *testing.T) {
	m := newTestManager(t, nil)
	a := mustCreate(t, m, "/bin/sh", "-c", "sleep 100")
	b := mustCreate(t, m, "/bin/sh", "-c", `trap "" HUP; echo ready; sleep 100`)
	collect(mustAttach(t, b, nil)).waitFor(t, "ready")
	ctx, cancel := contextWithTimeout(500 * time.Millisecond)
	defer cancel()
	m.Shutdown(ctx)
	ea, eb := waitExit(t, a).Exit, waitExit(t, b).Exit
	if ea.Signal == nil || *ea.Signal != "SIGHUP" || eb.Signal == nil || *eb.Signal != "SIGKILL" {
		t.Fatalf("exits %+v %+v", ea, eb)
	}
	if ea.InitiatedBy != "agent" {
		t.Fatalf("initiated_by %s", ea.InitiatedBy)
	}
	if _, err := m.Create(proto.CreateRequest{Kind: proto.KindShell, Argv: []string{"true"}}); err == nil {
		t.Fatal("create after shutdown succeeded")
	}
}

func TestCloseIfIdle(t *testing.T) {
	m := newTestManager(t, nil)
	s := mustCreate(t, m, "/bin/sh", "-c", "read x")
	if m.CloseIfIdle() {
		t.Fatal("closed with a session running")
	}
	s.Write([]byte("\r"))
	waitExit(t, s)
	if !m.CloseIfIdle() {
		t.Fatal("did not close once idle")
	}
	if _, err := m.Create(proto.CreateRequest{Kind: proto.KindShell, Argv: []string{"/bin/sh"}}); err == nil {
		t.Fatal("created a session after closing")
	}
}
