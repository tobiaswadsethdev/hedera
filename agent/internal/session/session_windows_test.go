//go:build windows

package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
)

// These run on the Windows CI runner against a real ConPTY.

func cmdArgv(script string) []string {
	return []string{"cmd.exe", "/d", "/c", script}
}

func TestWindowsRunAndExitCode(t *testing.T) {
	m := newTestManager(t, nil)
	sub, _, _, _ := m.Bus().Subscribe(nil)
	s := mustCreate(t, m, cmdArgv("echo hello-from-conpty& exit /b 3")...)
	c := collect(mustAttach(t, s, nil))
	snap := waitExit(t, s)
	if snap.Exit == nil || snap.Exit.Code == nil || *snap.Exit.Code != 3 || snap.Exit.InitiatedBy != "process" {
		t.Fatalf("exit %+v", snap.Exit)
	}
	c.waitEnd(t)
	if !strings.Contains(string(c.data), "hello-from-conpty") {
		t.Fatalf("output %q", c.data)
	}
	if got := eventTypes(sub, 3); !strings.HasSuffix(got, "session.exited") {
		t.Fatalf("events %s", got)
	}
}

func TestWindowsEnvironment(t *testing.T) {
	m := newTestManager(t, nil)
	s := mustCreate(t, m, cmdArgv("echo id=%HEDERA_PTY_ID% machine=%HEDERA_MACHINE_ID% term=%TERM%")...)
	c := collect(mustAttach(t, s, nil))
	c.waitFor(t, "id="+s.ID()+" machine=m_testtesttesttest term=xterm-256color")
	waitExit(t, s)
}

func TestWindowsInput(t *testing.T) {
	m := newTestManager(t, nil)
	s := mustCreate(t, m, cmdArgv("set /p X=ask: & call echo got-%X%")...)
	c := collect(mustAttach(t, s, nil))
	c.waitFor(t, "ask:")
	if err := s.Input("typed", true, nil); err != nil {
		t.Fatal(err)
	}
	c.waitFor(t, "got-typed")
	waitExit(t, s)
}

// longRunning keeps a child process (ping) busy so that closing has a whole
// process tree to take down.
var longRunning = cmdArgv("echo ready& ping -n 60 127.0.0.1 >nul")

func TestWindowsKillTakesTheTree(t *testing.T) {
	m := newTestManager(t, nil)
	s := mustCreate(t, m, longRunning...)
	collect(mustAttach(t, s, nil)).waitFor(t, "ready")
	start := time.Now()
	if err := s.Close("kill"); err != nil {
		t.Fatal(err)
	}
	snap := waitExit(t, s)
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("kill took %v", d)
	}
	if snap.Exit.InitiatedBy != "client" {
		t.Fatalf("exit %+v", snap.Exit)
	}
}

func TestWindowsHangup(t *testing.T) {
	m := newTestManager(t, func(c *Config) { c.HangupGrace = 3 * time.Second })
	s := mustCreate(t, m, longRunning...)
	collect(mustAttach(t, s, nil)).waitFor(t, "ready")
	if err := s.Close("hangup"); err != nil {
		t.Fatal(err)
	}
	// Either CTRL_CLOSE_EVENT ends it or the kill after the grace period
	// does; both count, as long as it ends.
	if snap := waitExit(t, s); snap.Exit.InitiatedBy != "client" {
		t.Fatalf("exit %+v", snap.Exit)
	}
}

func TestWindowsResize(t *testing.T) {
	m := newTestManager(t, nil)
	s, err := m.Create(proto.CreateRequest{Kind: proto.KindShell, Argv: cmdArgv("pause >nul & mode con"), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	c := collect(mustAttach(t, s, nil))
	time.Sleep(500 * time.Millisecond)
	if err := s.Resize(132, 40); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	s.Write([]byte("\r"))
	c.waitFor(t, "132")
	waitExit(t, s)
}

func TestWindowsShutdown(t *testing.T) {
	m := newTestManager(t, nil)
	a := mustCreate(t, m, longRunning...)
	collect(mustAttach(t, a, nil)).waitFor(t, "ready")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	m.Shutdown(ctx)
	if snap := waitExit(t, a); snap.Exit.InitiatedBy != "agent" {
		t.Fatalf("exit %+v", snap.Exit)
	}
}
