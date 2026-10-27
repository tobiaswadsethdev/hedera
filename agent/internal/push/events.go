package push

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
	"github.com/tobiaswadsethdev/hedera/agent/internal/session"
)

// recentInput is how recently someone must have typed into a session for its
// exit to count as deliberate: typing `exit` or `/exit` yourself shouldn't buzz
// your phone.
const recentInput = 10 * time.Second

// Source is what the event consumer needs from the agent.
type Source struct {
	Bus      *session.Bus
	BootID   string
	Hostname string
	// LastInput reports when a session last received input; zero if never
	// or unknown.
	LastInput func(sessionID string) time.Time
	Now       func() time.Time
}

// Notify maps an agent event to a notification. ok is false for events that
// don't notify. See docs/roadmap.md for the rules.
func Notify(ev proto.Event, src Source) (n Notification, ok bool) {
	s := ev.Session
	if s == nil {
		return n, false
	}
	n = Notification{
		EventID:   src.BootID + "-" + strconv.FormatUint(ev.Seq, 10),
		Title:     s.Title,
		Subtitle:  src.Hostname,
		SessionID: s.ID,
		Turn:      ev.Turn,
	}
	deliberate := func() bool {
		if src.LastInput == nil {
			return false
		}
		last := src.LastInput(s.ID)
		return !last.IsZero() && src.Now().Sub(last) < recentInput
	}
	switch ev.Type {
	case proto.EvSessionExited:
		// Claude sessions report their end through claude.ended instead.
		if s.Kind != proto.KindShell || s.Exit == nil || s.Exit.InitiatedBy != "process" || deliberate() {
			return n, false
		}
		n.Kind = KindShellExited
		n.Body = exitText("Exited", s.Exit)
	case proto.EvClaudeTurnFinished:
		n.Kind = KindClaudeTurnFinished
		n.Body = "Claude finished and is waiting for you"
		if ev.Preview != nil {
			n.Body = *ev.Preview
		}
	case proto.EvClaudeNeedsInput:
		n.Kind = KindClaudeNeedsInput
		n.Body = ev.Message
		if n.Body == "" {
			n.Body = "Claude needs your input"
		}
	case proto.EvClaudeFailed:
		n.Kind = KindClaudeFailed
		n.Body = "Claude hit an API error: " + ev.Error
		if ev.ErrorDetails != nil && *ev.ErrorDetails != "" {
			n.Body += " (" + *ev.ErrorDetails + ")"
		}
	case proto.EvClaudeEnded:
		if s.Exit != nil && s.Exit.InitiatedBy != "process" {
			return n, false // closed from a client, or the agent shut down
		}
		if deliberate() {
			return n, false
		}
		n.Kind = KindClaudeEnded
		n.Body = "Claude Code ended"
		if s.Exit != nil {
			if abnormal(s.Exit) {
				n.Kind = KindClaudeEndedAbnormally
			}
			n.Body = exitText("Claude Code exited", s.Exit)
		}
	default:
		return n, false
	}
	return n, true
}

func abnormal(e *proto.Exit) bool {
	return e.Signal != nil || (e.Code != nil && *e.Code != 0)
}

func exitText(prefix string, e *proto.Exit) string {
	switch {
	case e.Signal != nil:
		return fmt.Sprintf("%s: killed by %s", prefix, *e.Signal)
	case e.Code != nil && *e.Code != 0:
		return fmt.Sprintf("%s with code %d", prefix, *e.Code)
	}
	return prefix
}

// Watch feeds agent events into the pusher until ctx is done. If it falls
// behind the event bus it resubscribes from the last event it saw, so nothing
// still in the bus's log is missed.
func (p *Pusher) Watch(ctx context.Context, src Source) {
	if src.Now == nil {
		src.Now = time.Now
	}
	var since *uint64
	for ctx.Err() == nil {
		sub, backlog, seq, _ := src.Bus.Subscribe(since)
		last := seq
		handle := func(ev proto.Event) {
			last = ev.Seq
			if n, ok := Notify(ev, src); ok {
				if routes := p.Enqueue(n); routes > 0 {
					p.log.Debug("notification queued", "kind", n.Kind, "session", n.SessionID, "routes", routes)
				}
			}
		}
		for _, ev := range backlog {
			handle(ev)
		}
	loop:
		for {
			select {
			case <-ctx.Done():
				src.Bus.Unsubscribe(sub)
				return
			case ev, ok := <-sub.C:
				if !ok {
					break loop
				}
				handle(ev)
			}
		}
		p.log.Warn("push fell behind the event stream; catching up", "since", last)
		since = &last
	}
}
