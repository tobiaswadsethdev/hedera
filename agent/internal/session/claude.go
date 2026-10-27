package session

import (
	"strings"

	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
)

// HookEvent is the subset of a Claude Code hook payload the agent uses. See
// https://code.claude.com/docs/en/hooks for the full schemas.
type HookEvent struct {
	HookEventName        string  `json:"hook_event_name"`
	SessionID            string  `json:"session_id"`
	Source               string  `json:"source"`            // SessionStart
	Reason               string  `json:"reason"`            // SessionEnd
	NotificationType     string  `json:"notification_type"` // Notification
	Message              string  `json:"message"`           // Notification
	LastAssistantMessage string  `json:"last_assistant_message"`
	Error                string  `json:"error"` // StopFailure
	ErrorDetails         *string `json:"error_details"`
	ToolName             string  `json:"tool_name"` // PermissionRequest
}

// Hook events the agent reacts to; install-hooks registers exactly these.
const (
	HookSessionStart       = "SessionStart"
	HookUserPromptSubmit   = "UserPromptSubmit"
	HookPermissionRequest  = "PermissionRequest"
	HookPostToolUse        = "PostToolUse"
	HookPostToolUseFailure = "PostToolUseFailure"
	HookNotification       = "Notification"
	HookStop               = "Stop"
	HookStopFailure        = "StopFailure"
	HookSessionEnd         = "SessionEnd"
)

var knownHooks = map[string]bool{
	HookSessionStart: true, HookUserPromptSubmit: true, HookPermissionRequest: true,
	HookPostToolUse: true, HookPostToolUseFailure: true, HookNotification: true,
	HookStop: true, HookStopFailure: true, HookSessionEnd: true,
}

// HookNames lists the hook events the agent handles, in registration order.
var HookNames = []string{
	HookSessionStart, HookUserPromptSubmit, HookPermissionRequest, HookPostToolUse,
	HookPostToolUseFailure, HookNotification, HookStop, HookStopFailure, HookSessionEnd,
}

const previewRunes = 280

// claudeTracker is the Claude Code state of one session, plus the bookkeeping
// needed to dedupe notifications.
type claudeTracker struct {
	state proto.ClaudeState
	// rebind is set after SessionEnd(clear|resume): the process lives on and
	// the next SessionStart brings a new Claude session ID.
	rebind bool
	// finishedTurn is the turn a claude.turn_finished was sent for, or -1.
	finishedTurn int
	// inputNotified is set once claude.needs_input went out for the current
	// needs_input episode.
	inputNotified bool
}

func newClaudeTracker() *claudeTracker {
	return &claudeTracker{state: proto.ClaudeState{Status: proto.StatusStarting}, finishedTurn: -1}
}

func strp(s string) *string { return &s }

// preview collapses whitespace and trims text to a notification-sized
// snippet.
func preview(text string) *string {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return nil
	}
	if r := []rune(text); len(r) > previewRunes {
		text = string(r[:previewRunes-1]) + "…"
	}
	return &text
}

// ApplyHook feeds a Claude Code hook event into the session's Claude state
// and publishes the resulting events. Hooks may come from any process in the
// session (Claude Code started by hand in a shell session counts too).
func (s *Session) ApplyHook(ev HookEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == proto.StatusExited || !knownHooks[ev.HookEventName] {
		return
	}
	if s.claude == nil {
		s.claude = newClaudeTracker()
	}
	c := s.claude
	st := &c.state

	// One Claude session per PTY. Hooks from another Claude session ID are a
	// nested `claude` (e.g. `claude -p` run by a tool) and are ignored, except
	// a SessionStart that legitimately replaces the bound session.
	if st.SessionID != nil && *st.SessionID != ev.SessionID {
		replace := ev.HookEventName == HookSessionStart &&
			(c.rebind || st.Status == proto.ClaudeEnded || ev.Source == "clear" || ev.Source == "resume" || ev.Source == "fork")
		if !replace {
			s.log.Debug("ignoring hook from another claude session", "session", s.id, "event", ev.HookEventName)
			return
		}
	}

	var emit *proto.Event
	before := *st
	switch ev.HookEventName {
	case HookSessionStart:
		st.SessionID = strp(ev.SessionID)
		c.rebind = false
		st.EndReason = nil
		if ev.Source != "compact" {
			st.Status = proto.StatusIdle
			st.NeedsInput = nil
		}

	case HookUserPromptSubmit:
		s.bind(ev)
		st.Turn++
		st.Status = proto.StatusWorking
		st.NeedsInput = nil
		st.LastError = nil
		c.inputNotified = false

	case HookPermissionRequest:
		// Status only; the push-worthy Notification follows if nobody answers
		// within a few seconds.
		s.bind(ev)
		msg := "Claude needs permission"
		if ev.ToolName != "" {
			msg = "Claude wants to use " + ev.ToolName
		}
		if st.Status != proto.StatusNeedsInput {
			c.inputNotified = false
		}
		st.Status = proto.StatusNeedsInput
		st.NeedsInput = &proto.NeedsInput{Kind: "permission_prompt", Message: msg}

	case HookPostToolUse, HookPostToolUseFailure:
		// A tool ran, so any permission prompt was answered.
		s.bind(ev)
		if st.Status == proto.StatusNeedsInput {
			st.Status = proto.StatusWorking
			st.NeedsInput = nil
		}

	case HookNotification:
		s.bind(ev)
		switch ev.NotificationType {
		case "permission_prompt", "elicitation_dialog", "elicitation_url_dialog", "agent_needs_input":
			if st.Status == proto.StatusNeedsInput && c.inputNotified {
				break
			}
			st.Status = proto.StatusNeedsInput
			st.NeedsInput = &proto.NeedsInput{Kind: ev.NotificationType, Message: ev.Message}
			c.inputNotified = true
			emit = &proto.Event{Type: proto.EvClaudeNeedsInput, Kind: ev.NotificationType, Message: ev.Message}
		case "elicitation_complete", "elicitation_response":
			if st.Status == proto.StatusNeedsInput {
				st.Status = proto.StatusWorking
				st.NeedsInput = nil
			}
		case "idle_prompt":
			// Normally Stop already reported this turn; idle_prompt is the
			// one-minute reminder. Only act if Stop was missed.
			if c.finishedTurn == st.Turn {
				break
			}
			st.Status = proto.StatusIdle
			st.NeedsInput = nil
			c.finishedTurn = st.Turn
			emit = &proto.Event{Type: proto.EvClaudeTurnFinished}
		}

	case HookStop:
		s.bind(ev)
		st.Status = proto.StatusIdle
		st.NeedsInput = nil
		if p := preview(ev.LastAssistantMessage); p != nil {
			st.LastMessagePreview = p
		}
		c.finishedTurn = st.Turn
		emit = &proto.Event{Type: proto.EvClaudeTurnFinished, Preview: preview(ev.LastAssistantMessage)}

	case HookStopFailure:
		s.bind(ev)
		st.Status = proto.StatusFailed
		st.NeedsInput = nil
		st.LastError = &proto.ClaudeError{Error: ev.Error, ErrorDetails: ev.ErrorDetails}
		c.finishedTurn = st.Turn
		emit = &proto.Event{Type: proto.EvClaudeFailed, Error: ev.Error, ErrorDetails: ev.ErrorDetails}

	case HookSessionEnd:
		s.bind(ev)
		if ev.Reason == "clear" || ev.Reason == "resume" {
			c.rebind = true
			break
		}
		st.EndReason = strp(ev.Reason)
		st.Status = proto.ClaudeEnded
		st.NeedsInput = nil
		// A claude session's ended event waits for the process exit, so the
		// app gets one notification; Claude run inside a shell ends now.
		if s.kind == proto.KindShell {
			emit = &proto.Event{Type: proto.EvClaudeEnded, Reason: ev.Reason}
		}
	}

	s.syncStatusLocked()
	if emit == nil && claudeStateEqual(before, *st) {
		return
	}
	s.trackClaudeLocked()
	snap := s.snapshotLocked()
	if emit == nil {
		s.publishLocked(proto.Event{Type: proto.EvSessionUpdated, Session: &snap, Reason: "claude"})
		return
	}
	turn := st.Turn
	emit.Turn = &turn
	emit.Session = &snap
	s.publishLocked(*emit)
}

// bind records the Claude session ID from events that arrive before (or
// without) a SessionStart, e.g. when hooks were installed mid-session.
func (s *Session) bind(ev HookEvent) {
	if st := &s.claude.state; st.SessionID == nil && ev.SessionID != "" {
		st.SessionID = strp(ev.SessionID)
	}
}

// syncStatusLocked mirrors the Claude status into the session status for
// kind claude. Shell sessions keep "running"; their Claude state is only in
// the claude object.
func (s *Session) syncStatusLocked() {
	if s.kind != proto.KindClaude || s.claude == nil || s.status == proto.StatusExited {
		return
	}
	if cs := s.claude.state.Status; cs != proto.ClaudeEnded {
		s.status = cs
	}
}

// claudeEndedOnExitLocked is called once the process has exited.
func (s *Session) claudeEndedOnExitLocked() *proto.Event {
	if s.claude == nil {
		return nil
	}
	st := &s.claude.state
	alreadyEnded := st.Status == proto.ClaudeEnded
	st.Status = proto.ClaudeEnded
	st.NeedsInput = nil
	if st.EndReason == nil {
		st.EndReason = strp("exited")
	}
	if s.kind != proto.KindClaude || (alreadyEnded && s.kind == proto.KindShell) {
		return nil
	}
	turn := st.Turn
	return &proto.Event{Type: proto.EvClaudeEnded, Reason: *st.EndReason, Turn: &turn}
}

func claudeStateEqual(a, b proto.ClaudeState) bool {
	eqs := func(x, y *string) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }
	eqNI := func(x, y *proto.NeedsInput) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }
	eqErr := func(x, y *proto.ClaudeError) bool {
		return (x == nil) == (y == nil) && (x == nil || (x.Error == y.Error && eqs(x.ErrorDetails, y.ErrorDetails)))
	}
	return a.Status == b.Status && a.Turn == b.Turn && eqs(a.SessionID, b.SessionID) &&
		eqs(a.LastMessagePreview, b.LastMessagePreview) && eqs(a.EndReason, b.EndReason) &&
		eqNI(a.NeedsInput, b.NeedsInput) && eqErr(a.LastError, b.LastError)
}
