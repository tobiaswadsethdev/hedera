package session

import (
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
	"github.com/tobiaswadsethdev/hedera/agent/internal/ptyx"
)

const (
	readChunk   = 32 * 1024
	drainWait   = 500 * time.Millisecond
	submitDelay = 50 * time.Millisecond
)

// Session is one process on a PTY plus its scrollback and subscribers.
type Session struct {
	id        string
	kind      string
	cwd       string
	argv      []string
	createdAt time.Time
	hookToken string
	proc      ptyx.Process
	m         *Manager
	log       *slog.Logger

	writeMu sync.Mutex // serializes input so submit sequences stay intact

	mu        sync.Mutex
	ring      *Ring
	title     string
	size      proto.Size
	status    string
	exit      *proto.Exit
	exitTime  time.Time
	lastInput time.Time
	claude    *claudeTracker // kind claude, or once a hook arrives
	subs      map[*Subscriber]struct{}
	closing   string // who asked it to close: "client", "agent" or ""

	exitedCh chan struct{}
}

func (s *Session) ID() string { return s.id }

// HookToken is the per-session secret handed to the process as
// HEDERA_HOOK_TOKEN.
func (s *Session) HookToken() string { return s.hookToken }

// Exited is closed once the process has exited and all output is buffered.
func (s *Session) Exited() <-chan struct{} { return s.exitedCh }

// Snapshot returns the session's protocol representation.
func (s *Session) Snapshot() proto.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *Session) snapshotLocked() proto.Session {
	var exit *proto.Exit
	if s.exit != nil {
		e := *s.exit
		exit = &e
	}
	var claude *proto.ClaudeState
	if s.claude != nil {
		c := s.claude.state
		claude = &c
	}
	return proto.Session{
		ID:           s.id,
		Kind:         s.kind,
		Title:        s.title,
		Cwd:          s.cwd,
		Argv:         append([]string(nil), s.argv...),
		Pid:          s.proc.Pid(),
		CreatedAt:    proto.FormatTime(s.createdAt),
		Size:         s.size,
		Status:       s.status,
		OutputOffset: s.ring.Head(),
		Attached:     len(s.subs),
		Claude:       claude,
		Exit:         exit,
	}
}

func (s *Session) publishLocked(ev proto.Event) {
	s.m.bus.Publish(ev)
}

func (s *Session) publishUpdatedLocked(reason string) {
	snap := s.snapshotLocked()
	s.publishLocked(proto.Event{Type: proto.EvSessionUpdated, Session: &snap, Reason: reason})
}

// readLoop copies PTY output into the ring and to subscribers until the PTY
// reports an error (EOF/EIO once every holder of the terminal is gone, or
// Close).
func (s *Session) readLoop(done chan<- struct{}) {
	defer close(done)
	buf := make([]byte, readChunk)
	for {
		n, err := s.proc.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			s.appendOutput(data)
		}
		if err != nil {
			return
		}
	}
}

func (s *Session) appendOutput(data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	off := s.ring.Write(data)
	var lagged bool
	for sub := range s.subs {
		if !sub.push(off, data) {
			delete(s.subs, sub)
			lagged = true
		}
	}
	if lagged {
		s.publishUpdatedLocked("detached")
	}
}

// waitLoop waits for the process, lets the reader drain, then marks the
// session exited.
func (s *Session) waitLoop(readerDone <-chan struct{}) {
	st := s.proc.Wait()
	select {
	case <-readerDone:
	case <-time.After(drainWait):
		// Something else still holds the terminal (e.g. a background job).
		// Closing our side unblocks the reader.
		s.proc.Close()
		select {
		case <-readerDone:
		case <-time.After(drainWait):
			s.log.Warn("pty reader did not stop after close", "session", s.id)
		}
	}
	s.proc.Close()

	s.mu.Lock()
	s.exitTime = s.m.now()
	exit := &proto.Exit{Code: st.Code, At: proto.FormatTime(s.exitTime), InitiatedBy: "process"}
	if st.Signal != "" {
		sig := st.Signal
		exit.Signal = &sig
	}
	if s.closing != "" {
		exit.InitiatedBy = s.closing
	}
	s.exit = exit
	s.status = proto.StatusExited
	for sub := range s.subs {
		sub.finish(EndExited)
		delete(s.subs, sub)
	}
	if s.claude != nil && s.closing != "agent" {
		// Ended for real, not by the agent stopping: nothing to resume.
		s.m.resume.untrack(s.id)
	}
	ended := s.claudeEndedOnExitLocked()
	snap := s.snapshotLocked()
	s.publishLocked(proto.Event{Type: proto.EvSessionExited, Session: &snap})
	if ended != nil {
		ended.Session = &snap
		s.publishLocked(*ended)
	}
	s.mu.Unlock()

	s.log.Info("session exited", "session", s.id, "code", st.Code, "signal", st.Signal)
	s.m.onExit()
	close(s.exitedCh)
}

// Attach registers a subscriber and returns the replay it will receive first.
// See spec section 4.5 for the offset rules.
func (s *Session) Attach(from *uint64) (*Subscriber, proto.Replay, proto.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	base, head := s.ring.Base(), s.ring.Head()
	start := base
	truncated := base > 0
	if from != nil {
		switch {
		case *from >= base && *from <= head:
			start, truncated = *from, false
		default:
			truncated = true
		}
	}
	data := s.ring.ReadFrom(start)
	if truncated {
		skip := utf8Align(data)
		data = data[skip:]
		start += uint64(skip)
	}
	sub := newSubscriber(s.m.cfg.SubscriberLimit)
	sub.addReplay(start, data)
	if s.status == proto.StatusExited {
		sub.finish(EndExited)
	} else {
		s.subs[sub] = struct{}{}
		s.publishUpdatedLocked("attached")
	}
	return sub, proto.Replay{From: start, To: head, Truncated: truncated}, s.snapshotLocked()
}

// Detach stops delivery to sub. It is a no-op for an ended subscriber.
func (s *Session) Detach(sub *Subscriber) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs[sub]; !ok {
		return
	}
	delete(s.subs, sub)
	sub.finish(EndDetached)
	s.publishUpdatedLocked("detached")
}

func (s *Session) running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status != proto.StatusExited
}

// Write sends input to the process.
func (s *Session) Write(p []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.writeLocked(p)
}

func (s *Session) writeLocked(p []byte) error {
	if !s.running() {
		return proto.Errorf(proto.CodeSessionExited, "session has exited")
	}
	if _, err := s.proc.Write(p); err != nil {
		if !s.running() {
			return proto.Errorf(proto.CodeSessionExited, "session has exited")
		}
		return err
	}
	s.mu.Lock()
	s.lastInput = s.m.now()
	s.mu.Unlock()
	return nil
}

// LastInputAt is when input was last written to the session, from any
// client; zero if never.
func (s *Session) LastInputAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastInput
}

// Input writes text from a session.input request. With submit, it pauses
// briefly and then sends Enter as a separate write so TUIs don't treat the
// carriage return as part of a paste. ifTurn (Claude sessions only) makes it
// fail with CodeStale unless the Claude turn still has that value; the check
// and the write happen under one lock.
func (s *Session) Input(text string, submit bool, ifTurn *int) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if ifTurn != nil {
		s.mu.Lock()
		c := s.claude
		turn := 0
		if c != nil {
			turn = c.state.Turn
		}
		s.mu.Unlock()
		if c == nil {
			return proto.Errorf(proto.CodeInvalidArgument, "if_turn only applies to claude sessions")
		}
		if turn != *ifTurn {
			return proto.Errorf(proto.CodeStale, "the session has moved on to turn %d", turn)
		}
	}
	if err := s.writeLocked([]byte(text)); err != nil {
		return err
	}
	if !submit {
		return nil
	}
	time.Sleep(submitDelay)
	return s.writeLocked([]byte("\r"))
}

// Resize changes the PTY size. The most recent caller wins.
func (s *Session) Resize(cols, rows int) error {
	if err := validSize(cols, rows); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == proto.StatusExited {
		return proto.Errorf(proto.CodeSessionExited, "session has exited")
	}
	if s.size == (proto.Size{Cols: cols, Rows: rows}) {
		return nil
	}
	if err := s.proc.Resize(cols, rows); err != nil {
		return err
	}
	s.size = proto.Size{Cols: cols, Rows: rows}
	s.publishLocked(proto.Event{Type: proto.EvSessionResized, SessionID: s.id, Cols: cols, Rows: rows})
	return nil
}

// Close signals the process. mode is "hangup" (SIGHUP, then SIGKILL after the
// grace period) or "kill".
func (s *Session) Close(mode string) error {
	if mode == "" {
		mode = "hangup"
	}
	if mode != "hangup" && mode != "kill" {
		return proto.Errorf(proto.CodeInvalidArgument, "mode must be hangup or kill")
	}
	s.mu.Lock()
	if s.status == proto.StatusExited {
		s.mu.Unlock()
		return proto.Errorf(proto.CodeSessionExited, "session has exited")
	}
	s.closing = "client"
	s.mu.Unlock()

	if mode == "kill" {
		return ignoreDone(s.proc.Kill())
	}
	if err := ignoreDone(s.proc.Hangup()); err != nil {
		return err
	}
	go func() {
		select {
		case <-s.exitedCh:
		case <-time.After(s.m.cfg.HangupGrace):
			s.log.Info("session ignored hangup, killing", "session", s.id)
			s.proc.Kill()
		}
	}()
	return nil
}

func ignoreDone(err error) error {
	if errors.Is(err, errProcessDone) {
		return nil
	}
	return err
}

// end finishes every subscriber with reason; used on removal.
func (s *Session) end(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sub := range s.subs {
		sub.finish(reason)
		delete(s.subs, sub)
	}
}

func (s *Session) exitedAt() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitTime, s.exit != nil
}

func validSize(cols, rows int) error {
	if cols < 1 || cols > 1000 || rows < 1 || rows > 1000 {
		return proto.Errorf(proto.CodeInvalidArgument, "cols and rows must be 1-1000")
	}
	return nil
}
