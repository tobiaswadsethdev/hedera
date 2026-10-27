package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/tobiaswadsethdev/hedera/agent/internal/pairing"
	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
	"github.com/tobiaswadsethdev/hedera/agent/internal/session"
)

const (
	maxChannels    = 64
	maxInputLength = 64 * 1024
)

// Variables so tests can shorten them.
var (
	helloTimeout = 10 * time.Second
	writeTimeout = 10 * time.Second
	pingInterval = 20 * time.Second
	pongTimeout  = 10 * time.Second
)

// conn is one WebSocket client.
//
// The read loop handles requests in order and is the only goroutine that
// sends responses. Each attached channel has a writer goroutine for OUTPUT
// frames, and an events subscription has one for events. Write contexts are
// never cancelled early, because cancelling a write closes the connection.
type conn struct {
	srv       *Server
	ws        *websocket.Conn
	principal string
	log       *slog.Logger
	ctx       context.Context
	cancel    context.CancelFunc
	hello     atomic.Bool
	closeOnce sync.Once

	mu       sync.Mutex
	channels map[uint32]*channel
	nextCh   uint32
	events   *eventStream
}

type channel struct {
	id   uint32
	sess *session.Session
	sub  *session.Subscriber
	stop chan struct{}
	done chan struct{}
}

type eventStream struct {
	sub  *session.EventSub
	stop chan struct{}
	done chan struct{}
}

func newConn(s *Server, ws *websocket.Conn, principal string) *conn {
	ctx, cancel := context.WithCancel(context.Background())
	return &conn{
		srv:       s,
		ws:        ws,
		principal: principal,
		log:       s.Log.With("principal", principal),
		ctx:       ctx,
		cancel:    cancel,
		channels:  map[uint32]*channel{},
	}
}

// close sends a close frame asynchronously; the read loop then ends.
func (c *conn) close(code websocket.StatusCode, reason string) {
	c.closeOnce.Do(func() {
		go c.ws.Close(code, reason)
	})
}

func (c *conn) violation(reason string) {
	c.log.Warn("protocol violation", "reason", reason)
	c.close(proto.CloseProtocolViolation, reason)
}

func (c *conn) run(_ context.Context) {
	defer c.cleanup()
	c.ws.SetReadLimit(proto.MaxTextMessage)
	helloTimer := time.AfterFunc(helloTimeout, func() {
		if !c.hello.Load() {
			c.close(proto.CloseHandshake, "no hello")
		}
	})
	defer helloTimer.Stop()
	go c.pingLoop()

	for {
		typ, data, err := c.ws.Read(c.ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageBinary {
			if !c.hello.Load() {
				c.violation("binary frame before hello")
				continue
			}
			c.handleBinary(data)
			continue
		}
		c.handleText(data)
	}
}

func (c *conn) cleanup() {
	c.mu.Lock()
	chans := make([]*channel, 0, len(c.channels))
	for _, ch := range c.channels {
		chans = append(chans, ch)
	}
	es := c.events
	c.events = nil
	c.mu.Unlock()
	for _, ch := range chans {
		c.stopChannel(ch)
	}
	if es != nil {
		c.stopEvents(es)
	}
	c.cancel()
	c.ws.CloseNow()
}

func (c *conn) pingLoop() {
	t := time.NewTicker(pingInterval)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(c.ctx, pongTimeout)
			err := c.ws.Ping(ctx)
			cancel()
			if err != nil {
				c.log.Info("ping failed; dropping connection", "err", err)
				c.ws.CloseNow()
				return
			}
		}
	}
}

func (c *conn) send(typ websocket.MessageType, data []byte) error {
	ctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
	defer cancel()
	err := c.ws.Write(ctx, typ, data)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		c.log.Warn("write timed out; dropping slow client")
	}
	return err
}

func (c *conn) sendJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.send(websocket.MessageText, b)
}

func (c *conn) handleBinary(data []byte) {
	f, err := proto.DecodeFrame(data)
	if err != nil || f.Op != proto.OpInput {
		c.violation("bad binary frame")
		return
	}
	c.mu.Lock()
	ch := c.channels[f.Channel]
	c.mu.Unlock()
	if ch == nil {
		return // detached or never attached: ignored per spec
	}
	if err := ch.sess.Write(f.Payload); err != nil {
		c.log.Debug("input dropped", "session", ch.sess.ID(), "err", err)
	}
}

type handler func(c *conn, data []byte) (result any, after func(), err error)

var handlers = map[string]handler{
	proto.THello:             (*conn).handleHello,
	proto.TSessionList:       (*conn).handleList,
	proto.TSessionCreate:     (*conn).handleCreate,
	proto.TSessionAttach:     (*conn).handleAttach,
	proto.TSessionDetach:     (*conn).handleDetach,
	proto.TSessionInput:      (*conn).handleInput,
	proto.TSessionResize:     (*conn).handleResize,
	proto.TSessionClose:      (*conn).handleClose,
	proto.TSessionRemove:     (*conn).handleRemove,
	proto.TEventsSubscribe:   (*conn).handleSubscribe,
	proto.TEventsUnsubscribe: (*conn).handleUnsubscribe,
	proto.TPushConfigure:     (*conn).handlePushConfigure,
	proto.TPushStatus:        (*conn).handlePushStatus,
	proto.TPushRemove:        (*conn).handlePushRemove,
	proto.TPairingUnpair:     (*conn).handleUnpair,
	proto.TSessionResumable:  (*conn).handleResumable,
	proto.TSessionResume:     (*conn).handleResume,
	proto.TSessionForget:     (*conn).handleForget,
}

func (c *conn) handleText(data []byte) {
	var h proto.Header
	if err := json.Unmarshal(data, &h); err != nil || h.T == "" || h.ID == nil {
		c.violation("malformed control message")
		return
	}
	if !c.hello.Load() && h.T != proto.THello {
		c.violation("request before hello")
		return
	}
	var (
		result any
		after  func()
		err    error
	)
	if fn, ok := handlers[h.T]; ok {
		result, after, err = fn(c, data)
	} else {
		err = proto.Errorf(proto.CodeUnknownType, "unknown request type %q", h.T)
	}
	var out []byte
	if err != nil {
		pe := proto.AsError(err)
		if pe.Code == proto.CodeInternal {
			c.log.Error("request failed", "t", h.T, "err", err)
		}
		out = proto.MarshalError(*h.ID, pe)
	} else if out, err = proto.MarshalResult(*h.ID, result); err != nil {
		c.log.Error("encoding result", "t", h.T, "err", err)
		out = proto.MarshalError(*h.ID, proto.Errorf(proto.CodeInternal, "could not encode result"))
		after = nil
	}
	sendErr := c.send(websocket.MessageText, out)
	// Run after even if the send failed: it starts goroutines that cleanup
	// waits for, and they exit on their own once the connection is gone.
	if after != nil {
		after()
	}
	if sendErr != nil {
		return
	}
	if h.T == proto.THello && err != nil {
		c.close(proto.CloseHandshake, "unsupported version")
	}
}

func decode(data []byte, v any) error {
	if err := json.Unmarshal(data, v); err != nil {
		return proto.Errorf(proto.CodeInvalidArgument, "invalid parameters: %v", err)
	}
	return nil
}

func (c *conn) handleHello(data []byte) (any, func(), error) {
	if c.hello.Load() {
		return nil, nil, proto.Errorf(proto.CodeInvalidArgument, "hello was already sent")
	}
	var req proto.Hello
	if err := decode(data, &req); err != nil {
		return nil, nil, err
	}
	v := min(req.Protocol.Max, proto.Version)
	if v < max(req.Protocol.Min, proto.MinVersion) {
		return nil, nil, proto.Errorf(proto.CodeUnsupportedVersion, "agent speaks protocol %d..%d", proto.MinVersion, proto.Version)
	}
	c.hello.Store(true)
	c.log.Info("hello", "client", req.Client.Name, "client_version", req.Client.Version, "protocol", v)
	cfg := c.srv.Manager.Config()
	return proto.HelloResult{
		Protocol:  v,
		MachineID: c.srv.MachineID,
		BootID:    c.srv.BootID,
		Agent: proto.AgentInfo{
			Version:  c.srv.Version,
			OS:       runtime.GOOS,
			Arch:     runtime.GOARCH,
			Hostname: c.srv.Hostname,
		},
		Capabilities: c.srv.capabilities(),
		Limits: proto.Limits{
			MaxSessions:     cfg.MaxSessions,
			ScrollbackBytes: cfg.ScrollbackBytes,
			MaxFrame:        proto.MaxPayload,
		},
		ServerTime: proto.FormatTime(time.Now()),
	}, nil, nil
}

func (c *conn) handleList([]byte) (any, func(), error) {
	return proto.ListResult{Sessions: c.srv.Manager.List()}, nil, nil
}

func (c *conn) handleCreate(data []byte) (any, func(), error) {
	var req proto.CreateRequest
	if err := decode(data, &req); err != nil {
		return nil, nil, err
	}
	s, err := c.srv.Manager.Create(req)
	if err != nil {
		return nil, nil, err
	}
	return proto.CreateResult{Session: s.Snapshot()}, nil, nil
}

func (c *conn) handleAttach(data []byte) (any, func(), error) {
	var req proto.AttachRequest
	if err := decode(data, &req); err != nil {
		return nil, nil, err
	}
	if (req.Cols == 0) != (req.Rows == 0) {
		return nil, nil, proto.Errorf(proto.CodeInvalidArgument, "give both cols and rows, or neither")
	}
	s, err := c.srv.Manager.Get(req.SessionID)
	if err != nil {
		return nil, nil, err
	}
	c.mu.Lock()
	n := len(c.channels)
	c.mu.Unlock()
	if n >= maxChannels {
		return nil, nil, proto.Errorf(proto.CodeLimitExceeded, "at most %d channels per connection", maxChannels)
	}
	if req.Cols != 0 {
		if err := s.Resize(req.Cols, req.Rows); err != nil && proto.AsError(err).Code != proto.CodeSessionExited {
			return nil, nil, err
		}
	}
	sub, replay, snap := s.Attach(req.FromOffset)
	c.mu.Lock()
	c.nextCh++
	ch := &channel{id: c.nextCh, sess: s, sub: sub, stop: make(chan struct{}), done: make(chan struct{})}
	c.channels[ch.id] = ch
	c.mu.Unlock()
	// The writer starts after the response is sent, so no OUTPUT frame can
	// overtake it.
	return proto.AttachResult{Channel: ch.id, Session: snap, Replay: replay}, func() { go c.runChannel(ch) }, nil
}

func (c *conn) runChannel(ch *channel) {
	defer close(ch.done)
	buf := make([]byte, 0, 13+proto.MaxPayload)
	for {
		select {
		case <-ch.stop:
			return
		case <-c.ctx.Done():
			return
		case <-ch.sub.Ready():
		}
		chunks, end := ch.sub.Take()
		for _, k := range chunks {
			buf = proto.AppendOutput(buf[:0], ch.id, k.Offset, k.Data)
			if c.send(websocket.MessageBinary, buf) != nil {
				return
			}
		}
		if end == nil {
			continue
		}
		c.mu.Lock()
		delete(c.channels, ch.id)
		c.mu.Unlock()
		switch end.Reason {
		case session.EndLagged:
			c.log.Info("channel lagged", "session", ch.sess.ID(), "channel", ch.id)
			c.sendJSON(proto.Lagged{T: proto.TLagged, Channel: ch.id, SessionID: ch.sess.ID(), Offset: end.Offset})
		case session.EndExited, session.EndRemoved:
			c.sendJSON(proto.Detached{T: proto.TDetached, Channel: ch.id, SessionID: ch.sess.ID(), Reason: end.Reason})
		}
		return
	}
}

// stopChannel detaches ch and waits for its writer, so no OUTPUT frame for
// it is sent afterwards.
func (c *conn) stopChannel(ch *channel) {
	ch.sess.Detach(ch.sub)
	c.mu.Lock()
	if c.channels[ch.id] == ch {
		delete(c.channels, ch.id)
	}
	c.mu.Unlock()
	select {
	case <-ch.stop:
	default:
		close(ch.stop)
	}
	<-ch.done
}

func (c *conn) handleDetach(data []byte) (any, func(), error) {
	var req proto.DetachRequest
	if err := decode(data, &req); err != nil {
		return nil, nil, err
	}
	c.mu.Lock()
	ch := c.channels[req.Channel]
	c.mu.Unlock()
	if ch == nil {
		return nil, nil, proto.Errorf(proto.CodeNotFound, "no such channel")
	}
	c.stopChannel(ch)
	return nil, nil, nil
}

func (c *conn) handleInput(data []byte) (any, func(), error) {
	var req proto.InputRequest
	if err := decode(data, &req); err != nil {
		return nil, nil, err
	}
	if len(req.Data) > maxInputLength {
		return nil, nil, proto.Errorf(proto.CodeInvalidArgument, "data is longer than %d bytes", maxInputLength)
	}
	s, err := c.srv.Manager.Get(req.SessionID)
	if err != nil {
		return nil, nil, err
	}
	return nil, nil, s.Input(req.Data, req.Submit, req.IfTurn)
}

func (c *conn) handleResize(data []byte) (any, func(), error) {
	var req proto.ResizeRequest
	if err := decode(data, &req); err != nil {
		return nil, nil, err
	}
	s, err := c.srv.Manager.Get(req.SessionID)
	if err != nil {
		return nil, nil, err
	}
	return nil, nil, s.Resize(req.Cols, req.Rows)
}

func (c *conn) handleClose(data []byte) (any, func(), error) {
	var req proto.CloseRequest
	if err := decode(data, &req); err != nil {
		return nil, nil, err
	}
	s, err := c.srv.Manager.Get(req.SessionID)
	if err != nil {
		return nil, nil, err
	}
	if err := s.Close(req.Mode); err != nil {
		return nil, nil, err
	}
	c.log.Info("session close requested", "session", req.SessionID, "mode", req.Mode)
	return nil, nil, nil
}

func (c *conn) handleRemove(data []byte) (any, func(), error) {
	var req proto.RemoveRequest
	if err := decode(data, &req); err != nil {
		return nil, nil, err
	}
	return nil, nil, c.srv.Manager.Remove(req.SessionID)
}

func (c *conn) handleSubscribe(data []byte) (any, func(), error) {
	var req proto.SubscribeRequest
	if err := decode(data, &req); err != nil {
		return nil, nil, err
	}
	c.mu.Lock()
	old := c.events
	c.events = nil
	c.mu.Unlock()
	if old != nil {
		c.stopEvents(old)
	}
	sub, backlog, seq, reset := c.srv.Manager.Bus().Subscribe(req.SinceSeq)
	es := &eventStream{sub: sub, stop: make(chan struct{}), done: make(chan struct{})}
	c.mu.Lock()
	c.events = es
	c.mu.Unlock()
	return proto.SubscribeResult{Seq: seq, Reset: reset}, func() { go c.runEvents(es, backlog, seq) }, nil
}

func (c *conn) runEvents(es *eventStream, backlog []proto.Event, last uint64) {
	defer close(es.done)
	for _, ev := range backlog {
		if c.sendJSON(ev) != nil {
			return
		}
	}
	for {
		select {
		case <-es.stop:
			return
		case <-c.ctx.Done():
			return
		case ev, ok := <-es.sub.C:
			if !ok {
				if c.srv.Manager.Bus().Lagged(es.sub) {
					c.sendJSON(proto.EventsLagged{T: proto.TEventsLagged, Seq: last})
				}
				return
			}
			if c.sendJSON(ev) != nil {
				return
			}
			last = ev.Seq
		}
	}
}

func (c *conn) stopEvents(es *eventStream) {
	c.srv.Manager.Bus().Unsubscribe(es.sub)
	close(es.stop)
	<-es.done
}

func (c *conn) handleUnsubscribe([]byte) (any, func(), error) {
	c.mu.Lock()
	es := c.events
	c.events = nil
	c.mu.Unlock()
	if es != nil {
		c.stopEvents(es)
	}
	return nil, nil, nil
}

// Hostname returns the machine's short hostname for display.
func Hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	for i := range h {
		if h[i] == '.' {
			return h[:i]
		}
	}
	return h
}

func (s *Server) capabilities() []string {
	caps := []string{"shell", "claude"}
	if s.Push != nil {
		caps = append(caps, "push")
	}
	return caps
}

func (c *conn) pushEnabled() error {
	if c.srv.Push == nil {
		return proto.Errorf(proto.CodeUnknownType, "push is not enabled on this agent")
	}
	return nil
}

// Push routes are keyed by the connection's principal: each paired device
// configures its own.
func (c *conn) handlePushConfigure(data []byte) (any, func(), error) {
	if err := c.pushEnabled(); err != nil {
		return nil, nil, err
	}
	var req proto.PushConfigureRequest
	if err := decode(data, &req); err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(c.ctx, 15*time.Second)
	defer cancel()
	if _, err := c.srv.Push.Configure(ctx, c.principal, req.RadixURL, req.PushToken, req.Prefs); err != nil {
		return nil, nil, err
	}
	return c.srv.Push.Status(c.principal), nil, nil
}

func (c *conn) handlePushStatus([]byte) (any, func(), error) {
	if err := c.pushEnabled(); err != nil {
		return nil, nil, err
	}
	return c.srv.Push.Status(c.principal), nil, nil
}

func (c *conn) handlePushRemove([]byte) (any, func(), error) {
	if err := c.pushEnabled(); err != nil {
		return nil, nil, err
	}
	c.srv.Push.Remove(c.principal)
	return nil, nil, nil
}

// handleUnpair lets a paired device remove itself, e.g. when the user deletes
// the machine in the app: its token stops working, its push route goes, and
// every connection using it is closed with 4003 once the response is sent.
func (c *conn) handleUnpair([]byte) (any, func(), error) {
	if c.principal == LocalPrincipal {
		return nil, nil, proto.Errorf(proto.CodeInvalidArgument, "only a paired device can unpair; use 'hedera tokens revoke' locally")
	}
	if err := c.srv.Pairings.Revoke(c.principal); err != nil && !errors.Is(err, pairing.ErrNotFound) {
		return nil, nil, err
	}
	if c.srv.Push != nil {
		c.srv.Push.Remove(c.principal)
	}
	c.log.Info("device unpaired itself")
	return nil, func() { c.srv.CloseConnections(c.principal, proto.CloseRevoked, "unpaired") }, nil
}

func (c *conn) handleResumable([]byte) (any, func(), error) {
	return proto.ResumableResult{Resumable: c.srv.Manager.Resumable()}, nil, nil
}

func (c *conn) handleResume(data []byte) (any, func(), error) {
	var req proto.ResumeRequest
	if err := decode(data, &req); err != nil {
		return nil, nil, err
	}
	s, err := c.srv.Manager.Resume(req.ClaudeSessionID, req.Cols, req.Rows)
	if err != nil {
		return nil, nil, err
	}
	c.log.Info("claude session resumed", "session", s.ID())
	return proto.CreateResult{Session: s.Snapshot()}, nil, nil
}

func (c *conn) handleForget(data []byte) (any, func(), error) {
	var req proto.ForgetRequest
	if err := decode(data, &req); err != nil {
		return nil, nil, err
	}
	return nil, nil, c.srv.Manager.Forget(req.ClaudeSessionID)
}
