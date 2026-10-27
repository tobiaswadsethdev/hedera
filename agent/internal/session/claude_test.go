//go:build !windows

package session

import (
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
)

// drain returns the events published so far.
func drain(sub *EventSub) []proto.Event {
	var out []proto.Event
	for {
		select {
		case ev := <-sub.C:
			out = append(out, ev)
		default:
			return out
		}
	}
}

// claudeEvents returns only the claude.* event types.
func claudeEvents(evs []proto.Event) string {
	var types []string
	for _, ev := range evs {
		if strings.HasPrefix(ev.Type, "claude.") {
			types = append(types, ev.Type)
		}
	}
	return strings.Join(types, ",")
}

func newClaudeSession(t *testing.T, m *Manager, kind string) (*Session, *EventSub) {
	t.Helper()
	s, err := m.Create(proto.CreateRequest{Kind: kind, Argv: []string{"/bin/sh", "-c", "read x"}})
	if err != nil {
		t.Fatal(err)
	}
	sub, _, _, _ := m.Bus().Subscribe(nil)
	return s, sub
}

func hook(name string, mod ...func(*HookEvent)) HookEvent {
	ev := HookEvent{HookEventName: name, SessionID: "abc"}
	for _, f := range mod {
		f(&ev)
	}
	return ev
}

func notif(kind, msg string) func(*HookEvent) {
	return func(e *HookEvent) { e.NotificationType = kind; e.Message = msg }
}

func TestClaudeTurnLifecycle(t *testing.T) {
	m := newTestManager(t, nil)
	s, sub := newClaudeSession(t, m, proto.KindClaude)

	s.ApplyHook(hook(HookSessionStart, func(e *HookEvent) { e.Source = "startup" }))
	snap := s.Snapshot()
	if snap.Status != proto.StatusIdle || *snap.Claude.SessionID != "abc" || snap.Claude.Status != proto.StatusIdle {
		t.Fatalf("after SessionStart: %+v %+v", snap.Status, snap.Claude)
	}

	s.ApplyHook(hook(HookUserPromptSubmit))
	if snap = s.Snapshot(); snap.Status != proto.StatusWorking || snap.Claude.Turn != 1 {
		t.Fatalf("after prompt: %s turn %d", snap.Status, snap.Claude.Turn)
	}

	s.ApplyHook(hook(HookPermissionRequest, func(e *HookEvent) { e.ToolName = "Bash" }))
	snap = s.Snapshot()
	if snap.Status != proto.StatusNeedsInput || snap.Claude.NeedsInput.Message != "Claude wants to use Bash" {
		t.Fatalf("after PermissionRequest: %s %+v", snap.Status, snap.Claude.NeedsInput)
	}
	if got := claudeEvents(drain(sub)); got != "" {
		t.Fatalf("PermissionRequest alone must not notify, got %s", got)
	}

	s.ApplyHook(hook(HookNotification, notif("permission_prompt", "Claude needs your permission to use Bash")))
	s.ApplyHook(hook(HookNotification, notif("permission_prompt", "Claude needs your permission to use Bash")))
	evs := drain(sub)
	if got := claudeEvents(evs); got != "claude.needs_input" {
		t.Fatalf("needs_input events: %s", got)
	}
	ni := evs[len(evs)-1]
	if ni.Kind != "permission_prompt" || *ni.Turn != 1 || ni.Session == nil || ni.Session.Status != proto.StatusNeedsInput {
		t.Fatalf("needs_input event %+v", ni)
	}

	s.ApplyHook(hook(HookPostToolUse))
	if snap = s.Snapshot(); snap.Status != proto.StatusWorking || snap.Claude.NeedsInput != nil {
		t.Fatalf("after PostToolUse: %s", snap.Status)
	}

	s.ApplyHook(hook(HookStop, func(e *HookEvent) { e.LastAssistantMessage = "Done.\n\nAll   tests pass." }))
	evs = drain(sub)
	if got := claudeEvents(evs); got != "claude.turn_finished" {
		t.Fatalf("stop events: %s", got)
	}
	tf := evs[len(evs)-1]
	if *tf.Preview != "Done. All tests pass." || *tf.Turn != 1 {
		t.Fatalf("turn_finished %+v", tf)
	}
	if snap = s.Snapshot(); snap.Status != proto.StatusIdle || *snap.Claude.LastMessagePreview != "Done. All tests pass." {
		t.Fatalf("after Stop: %s", snap.Status)
	}

	// The one-minute idle reminder doesn't notify again.
	s.ApplyHook(hook(HookNotification, notif("idle_prompt", "Claude is waiting for your input")))
	if got := claudeEvents(drain(sub)); got != "" {
		t.Fatalf("idle_prompt after Stop notified: %s", got)
	}
}

func TestClaudeIdlePromptWithoutStop(t *testing.T) {
	m := newTestManager(t, nil)
	s, sub := newClaudeSession(t, m, proto.KindClaude)
	s.ApplyHook(hook(HookUserPromptSubmit))
	s.ApplyHook(hook(HookNotification, notif("idle_prompt", "waiting")))
	s.ApplyHook(hook(HookNotification, notif("idle_prompt", "waiting")))
	if got := claudeEvents(drain(sub)); got != "claude.turn_finished" {
		t.Fatalf("events %s", got)
	}
	if s.Snapshot().Status != proto.StatusIdle {
		t.Fatal("not idle")
	}
}

func TestClaudeFailure(t *testing.T) {
	m := newTestManager(t, nil)
	s, sub := newClaudeSession(t, m, proto.KindClaude)
	s.ApplyHook(hook(HookUserPromptSubmit))
	details := "429 Too Many Requests"
	s.ApplyHook(hook(HookStopFailure, func(e *HookEvent) { e.Error = "rate_limit"; e.ErrorDetails = &details }))
	evs := drain(sub)
	if got := claudeEvents(evs); got != "claude.failed" {
		t.Fatalf("events %s", got)
	}
	if f := evs[len(evs)-1]; f.Error != "rate_limit" || *f.ErrorDetails != details {
		t.Fatalf("failed event %+v", f)
	}
	snap := s.Snapshot()
	if snap.Status != proto.StatusFailed || snap.Claude.LastError.Error != "rate_limit" {
		t.Fatalf("after failure %s %+v", snap.Status, snap.Claude.LastError)
	}
	s.ApplyHook(hook(HookUserPromptSubmit))
	if snap = s.Snapshot(); snap.Status != proto.StatusWorking || snap.Claude.LastError != nil || snap.Claude.Turn != 2 {
		t.Fatalf("retry %s %+v", snap.Status, snap.Claude)
	}
}

func TestClaudeIgnoresNestedSession(t *testing.T) {
	m := newTestManager(t, nil)
	s, sub := newClaudeSession(t, m, proto.KindClaude)
	s.ApplyHook(hook(HookSessionStart, func(e *HookEvent) { e.Source = "startup" }))
	s.ApplyHook(hook(HookUserPromptSubmit))
	drain(sub)

	nested := func(e *HookEvent) { e.SessionID = "nested" }
	s.ApplyHook(hook(HookSessionStart, nested, func(e *HookEvent) { e.Source = "startup" }))
	s.ApplyHook(hook(HookUserPromptSubmit, nested))
	s.ApplyHook(hook(HookStop, nested))
	s.ApplyHook(hook(HookSessionEnd, nested, func(e *HookEvent) { e.Reason = "other" }))
	if evs := drain(sub); len(evs) != 0 {
		t.Fatalf("nested session produced events: %+v", evs)
	}
	snap := s.Snapshot()
	if *snap.Claude.SessionID != "abc" || snap.Claude.Turn != 1 || snap.Status != proto.StatusWorking {
		t.Fatalf("state changed: %+v", snap.Claude)
	}
}

func TestClaudeClearRebinds(t *testing.T) {
	m := newTestManager(t, nil)
	s, sub := newClaudeSession(t, m, proto.KindClaude)
	s.ApplyHook(hook(HookSessionStart, func(e *HookEvent) { e.Source = "startup" }))
	s.ApplyHook(hook(HookUserPromptSubmit))
	s.ApplyHook(hook(HookStop))
	drain(sub)

	s.ApplyHook(hook(HookSessionEnd, func(e *HookEvent) { e.Reason = "clear" }))
	s.ApplyHook(hook(HookSessionStart, func(e *HookEvent) { e.SessionID = "fresh"; e.Source = "clear" }))
	if got := claudeEvents(drain(sub)); got != "" {
		t.Fatalf("/clear produced %s", got)
	}
	snap := s.Snapshot()
	if *snap.Claude.SessionID != "fresh" || snap.Status != proto.StatusIdle || snap.Claude.EndReason != nil {
		t.Fatalf("after /clear %+v", snap.Claude)
	}
	// Turns keep counting so an old if_turn can never match again.
	s.ApplyHook(hook(HookUserPromptSubmit, func(e *HookEvent) { e.SessionID = "fresh" }))
	if s.Snapshot().Claude.Turn != 2 {
		t.Fatal("turn counter reset by /clear")
	}
}

func TestClaudeEndedMergesWithExit(t *testing.T) {
	m := newTestManager(t, nil)
	s, sub := newClaudeSession(t, m, proto.KindClaude)
	s.ApplyHook(hook(HookSessionStart, func(e *HookEvent) { e.Source = "startup" }))
	s.ApplyHook(hook(HookSessionEnd, func(e *HookEvent) { e.Reason = "prompt_input_exit" }))
	if got := claudeEvents(drain(sub)); got != "" {
		t.Fatalf("SessionEnd notified before exit: %s", got)
	}
	s.Write([]byte("\r"))
	waitExit(t, s)
	evs := drain(sub)
	var types []string
	for _, ev := range evs {
		types = append(types, ev.Type)
	}
	if got := strings.Join(types, ","); !strings.HasSuffix(got, "session.exited,claude.ended") {
		t.Fatalf("events %s", got)
	}
	ended := evs[len(evs)-1]
	if ended.Reason != "prompt_input_exit" || ended.Session.Exit == nil || ended.Session.Claude.Status != proto.ClaudeEnded {
		t.Fatalf("ended %+v", ended)
	}
	// Hooks after exit are ignored.
	s.ApplyHook(hook(HookStop))
	if evs := drain(sub); len(evs) != 0 {
		t.Fatalf("hook after exit: %+v", evs)
	}
}

func TestClaudeEndedWithoutSessionEnd(t *testing.T) {
	m := newTestManager(t, nil)
	s, sub := newClaudeSession(t, m, proto.KindClaude)
	s.Close("kill")
	waitExit(t, s)
	evs := drain(sub)
	if got := claudeEvents(evs); got != "claude.ended" {
		t.Fatalf("events %s", got)
	}
	if r := evs[len(evs)-1]; r.Reason != "exited" || r.Session.Exit.InitiatedBy != "client" {
		t.Fatalf("ended %+v", r)
	}
}

func TestClaudeInsideShell(t *testing.T) {
	m := newTestManager(t, nil)
	s, sub := newClaudeSession(t, m, proto.KindShell)
	if s.Snapshot().Claude != nil {
		t.Fatal("shell has claude state before any hook")
	}
	s.ApplyHook(hook(HookSessionStart, func(e *HookEvent) { e.Source = "startup" }))
	s.ApplyHook(hook(HookUserPromptSubmit))
	s.ApplyHook(hook(HookStop, func(e *HookEvent) { e.LastAssistantMessage = "hi" }))
	snap := s.Snapshot()
	if snap.Status != proto.StatusRunning || snap.Claude == nil || snap.Claude.Status != proto.StatusIdle {
		t.Fatalf("shell snapshot %s %+v", snap.Status, snap.Claude)
	}
	s.ApplyHook(hook(HookSessionEnd, func(e *HookEvent) { e.Reason = "other" }))
	if got := claudeEvents(drain(sub)); got != "claude.turn_finished,claude.ended" {
		t.Fatalf("events %s", got)
	}
	turn := 1
	if err := s.Input("", false, &turn); err != nil {
		t.Fatalf("if_turn on a shell running claude: %v", err)
	}
	// Claude starting again in the same shell rebinds.
	s.ApplyHook(hook(HookSessionStart, func(e *HookEvent) { e.SessionID = "second"; e.Source = "startup" }))
	if snap := s.Snapshot(); *snap.Claude.SessionID != "second" || snap.Claude.Status != proto.StatusIdle {
		t.Fatalf("rebind %+v", snap.Claude)
	}
	s.Write([]byte("\r"))
	waitExit(t, s)
	if got := claudeEvents(drain(sub)); got != "" {
		t.Fatalf("shell exit produced %s", got)
	}
}

func TestClaudeStaleReply(t *testing.T) {
	m := newTestManager(t, nil)
	s, _ := newClaudeSession(t, m, proto.KindClaude)
	s.ApplyHook(hook(HookUserPromptSubmit))
	s.ApplyHook(hook(HookStop))
	replyTo := s.Snapshot().Claude.Turn
	s.ApplyHook(hook(HookUserPromptSubmit)) // answered at the desk meanwhile
	var pe *proto.Error
	if err := s.Input("late reply", true, &replyTo); !errors.As(err, &pe) || pe.Code != proto.CodeStale {
		t.Fatalf("stale reply: %v", err)
	}
}

func TestUnknownHookIgnored(t *testing.T) {
	m := newTestManager(t, nil)
	s, sub := newClaudeSession(t, m, proto.KindShell)
	s.ApplyHook(hook("PreCompact"))
	if len(drain(sub)) != 0 || s.Snapshot().Claude != nil {
		t.Fatal("unknown hook changed the session")
	}
}

func TestPreview(t *testing.T) {
	if preview("  \n ") != nil {
		t.Fatal("blank preview")
	}
	long := strings.Repeat("ä", 400)
	p := preview(long)
	if r := []rune(*p); len(r) != previewRunes || r[len(r)-1] != '…' {
		t.Fatalf("len %d", len(r))
	}
}

func TestExitLogShowsTheCode(t *testing.T) {
	// The exit is logged before Exited() closes, so reading after waitExit
	// is race-free.
	var buf strings.Builder
	m := newTestManager(t, func(c *Config) { c.Logger = slog.New(slog.NewTextHandler(&buf, nil)) })
	waitExit(t, mustCreate(t, m, "/bin/sh", "-c", "exit 7"))
	if out := buf.String(); !strings.Contains(out, `msg="session exited"`) || !strings.Contains(out, "code=7") {
		t.Fatalf("log: %s", out)
	}
}
