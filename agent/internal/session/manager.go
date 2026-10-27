// Package session owns PTY sessions: spawning, scrollback, attach/replay,
// input, resize, close, exit detection, and the event stream.
package session

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tobiaswadsethdev/hedera/agent/internal/ids"
	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
	"github.com/tobiaswadsethdev/hedera/agent/internal/ptyx"
)

var errProcessDone = os.ErrProcessDone

// Config tunes a Manager. Zero values get the defaults noted.
type Config struct {
	MachineID       string
	ScrollbackBytes int           // per session; default 4 MiB
	MaxSessions     int           // running sessions; default 32
	MaxExited       int           // exited sessions kept; default 32
	ExitedTTL       time.Duration // default 24h
	SubscriberLimit int           // live bytes queued per subscriber; default 1 MiB
	HangupGrace     time.Duration // SIGHUP → SIGKILL; default 5s

	// Spawn starts processes; default ptyx.Start.
	Spawn func(ptyx.Options) (ptyx.Process, error)
	// Resolve turns a request into the argv to display and the argv to exec;
	// default ResolveCommand.
	Resolve func(kind string, argv []string) (display, exec []string)
	// BaseEnv is the environment processes inherit; default os.Environ().
	BaseEnv func() []string
	// Home is the default working directory; default os.UserHomeDir().
	Home string
	// HookURL is where `hedera hook` posts Claude Code hook events, handed to
	// processes as HEDERA_HOOK_URL.
	HookURL string

	Logger *slog.Logger
	Now    func() time.Time
}

// Manager owns all sessions on this machine.
type Manager struct {
	cfg Config
	bus *Bus
	log *slog.Logger
	now func() time.Time

	mu       sync.Mutex
	sessions map[string]*Session
	starting int // creates past the limit check that are not in sessions yet
	closed   bool
}

func NewManager(cfg Config) *Manager {
	if cfg.ScrollbackBytes <= 0 {
		cfg.ScrollbackBytes = 4 << 20
	}
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = 32
	}
	if cfg.MaxExited <= 0 {
		cfg.MaxExited = 32
	}
	if cfg.ExitedTTL <= 0 {
		cfg.ExitedTTL = 24 * time.Hour
	}
	if cfg.SubscriberLimit <= 0 {
		cfg.SubscriberLimit = 1 << 20
	}
	if cfg.HangupGrace <= 0 {
		cfg.HangupGrace = 5 * time.Second
	}
	if cfg.Spawn == nil {
		cfg.Spawn = ptyx.Start
	}
	if cfg.Resolve == nil {
		cfg.Resolve = ResolveCommand
	}
	if cfg.BaseEnv == nil {
		cfg.BaseEnv = os.Environ
	}
	if cfg.Home == "" {
		cfg.Home, _ = os.UserHomeDir()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Manager{
		cfg:      cfg,
		bus:      NewBus(0, 0, cfg.Now),
		log:      cfg.Logger,
		now:      cfg.Now,
		sessions: map[string]*Session{},
	}
}

func (m *Manager) Bus() *Bus      { return m.bus }
func (m *Manager) Config() Config { return m.cfg }

// ResolveCommand picks the default command for kind and wraps it in the
// user's login shell. The display argv is what clients see.
func ResolveCommand(kind string, argv []string) (display, exec []string) {
	switch {
	case len(argv) > 0:
		display = argv
	case kind == proto.KindClaude:
		display = []string{"claude"}
	default:
		// The default shell is already a login shell; no wrapping needed.
		sh := ptyx.DefaultShell()
		return sh, sh
	}
	return display, ptyx.WrapLogin(display)
}

// Create spawns a new session.
func (m *Manager) Create(req proto.CreateRequest) (*Session, error) {
	if req.Kind != proto.KindShell && req.Kind != proto.KindClaude {
		return nil, proto.Errorf(proto.CodeInvalidArgument, "kind must be shell or claude")
	}
	cols, rows := req.Cols, req.Rows
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	if err := validSize(cols, rows); err != nil {
		return nil, err
	}
	if len(req.Title) > 200 {
		return nil, proto.Errorf(proto.CodeInvalidArgument, "title is longer than 200 characters")
	}
	for k := range req.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") {
			return nil, proto.Errorf(proto.CodeInvalidArgument, "invalid environment variable name %q", k)
		}
		if strings.HasPrefix(k, "HEDERA_") {
			return nil, proto.Errorf(proto.CodeInvalidArgument, "environment variables starting with HEDERA_ are reserved")
		}
	}
	for _, a := range req.Argv {
		if strings.ContainsRune(a, 0) {
			return nil, proto.Errorf(proto.CodeInvalidArgument, "argv contains a NUL byte")
		}
	}
	cwd, err := m.resolveCwd(req.Cwd)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, proto.Errorf(proto.CodeInternal, "agent is shutting down")
	}
	if m.runningLocked()+m.starting >= m.cfg.MaxSessions {
		m.mu.Unlock()
		return nil, proto.Errorf(proto.CodeLimitExceeded, "at most %d sessions can run at once", m.cfg.MaxSessions)
	}
	m.starting++
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.starting--
		m.mu.Unlock()
	}()

	display, execArgv := m.cfg.Resolve(req.Kind, req.Argv)
	s := &Session{
		id:        ids.SessionID(),
		kind:      req.Kind,
		cwd:       cwd,
		argv:      display,
		createdAt: m.now(),
		hookToken: ids.Secret("hht_"),
		m:         m,
		log:       m.log,
		ring:      NewRing(m.cfg.ScrollbackBytes),
		title:     req.Title,
		size:      proto.Size{Cols: cols, Rows: rows},
		status:    proto.StatusRunning,
		subs:      map[*Subscriber]struct{}{},
		exitedCh:  make(chan struct{}),
	}
	if req.Kind == proto.KindClaude {
		// Hooks (Phase 2) move it on from here.
		s.status = proto.StatusStarting
		s.claude = newClaudeTracker()
	}
	if s.title == "" {
		s.title = defaultTitle(display, cwd, m.cfg.Home)
	}
	proc, err := m.cfg.Spawn(ptyx.Options{
		Argv: execArgv,
		Dir:  cwd,
		Env:  m.buildEnv(s, req.Env),
		Cols: cols,
		Rows: rows,
	})
	if err != nil {
		return nil, proto.Errorf(proto.CodeInvalidArgument, "could not start %s: %v", display[0], err)
	}
	s.proc = proc

	m.mu.Lock()
	for m.sessions[s.id] != nil {
		s.id = ids.SessionID() // 50 random bits; a collision is theoretical
	}
	m.sessions[s.id] = s
	m.mu.Unlock()

	readerDone := make(chan struct{})
	go s.readLoop(readerDone)
	go s.waitLoop(readerDone)

	s.mu.Lock()
	snap := s.snapshotLocked()
	s.publishLocked(proto.Event{Type: proto.EvSessionCreated, Session: &snap})
	s.mu.Unlock()
	m.log.Info("session created", "session", s.id, "kind", req.Kind, "pid", proc.Pid())
	return s, nil
}

func (m *Manager) resolveCwd(cwd string) (string, error) {
	switch {
	case cwd == "" || cwd == "~":
		cwd = m.cfg.Home
	case strings.HasPrefix(cwd, "~/"):
		cwd = filepath.Join(m.cfg.Home, cwd[2:])
	}
	if !filepath.IsAbs(cwd) {
		return "", proto.Errorf(proto.CodeInvalidArgument, "cwd must be absolute or start with ~")
	}
	fi, err := os.Stat(cwd)
	if err != nil || !fi.IsDir() {
		return "", proto.Errorf(proto.CodeInvalidArgument, "cwd %s is not a directory", cwd)
	}
	return filepath.Clean(cwd), nil
}

func (m *Manager) buildEnv(s *Session, extra map[string]string) []string {
	var env []string
	hasLocale := false
	for _, kv := range m.cfg.BaseEnv() {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case sessionVars[k], claudeMarkers[k]:
			// Per-process identity of whoever started the agent; a new
			// terminal must not look like a child of it.
			continue
		case k == "TERM" || k == "COLORTERM":
			continue
		case k == "LANG" || k == "LC_ALL" || k == "LC_CTYPE":
			hasLocale = true
		}
		if _, overridden := extra[k]; overridden {
			continue
		}
		env = append(env, kv)
	}
	if loc := ptyx.DefaultLocale(); !hasLocale && loc != "" {
		if _, ok := extra["LANG"]; !ok {
			env = append(env, "LANG="+loc)
		}
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+extra[k])
	}
	env = append(env,
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"HEDERA_PTY_ID="+s.id,
		"HEDERA_MACHINE_ID="+m.cfg.MachineID,
		"HEDERA_HOOK_TOKEN="+s.hookToken,
	)
	if m.cfg.HookURL != "" {
		env = append(env, "HEDERA_HOOK_URL="+m.cfg.HookURL)
	}
	return env
}

// claudeMarkers tell Claude Code it runs inside another Claude Code session.
// They leak in when `hedera run` is started from a Claude Code terminal.
// Configuration variables (CLAUDE_CODE_USE_BEDROCK etc.) are kept.
var claudeMarkers = map[string]bool{
	"CLAUDECODE": true, "CLAUDE_CODE_CHILD_SESSION": true, "CLAUDE_CODE_SESSION_ID": true,
	"CLAUDE_CODE_ENTRYPOINT": true, "CLAUDE_PID": true,
}

// sessionVars are the per-session variables the agent sets.
var sessionVars = map[string]bool{
	"HEDERA_PTY_ID": true, "HEDERA_MACHINE_ID": true, "HEDERA_HOOK_TOKEN": true, "HEDERA_HOOK_URL": true,
}

func defaultTitle(argv []string, cwd, home string) string {
	dir := filepath.Base(cwd)
	if cwd == home {
		dir = "~"
	}
	return fmt.Sprintf("%s — %s", filepath.Base(argv[0]), dir)
}

func (m *Manager) runningLocked() int {
	n := 0
	for _, s := range m.sessions {
		if s.running() {
			n++
		}
	}
	return n
}

// Get returns a session by ID.
func (m *Manager) Get(id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, proto.Errorf(proto.CodeNotFound, "no such session")
	}
	return s, nil
}

// List returns snapshots of all sessions, oldest first.
func (m *Manager) List() []proto.Session {
	m.mu.Lock()
	all := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.Unlock()
	sort.Slice(all, func(i, j int) bool {
		if !all[i].createdAt.Equal(all[j].createdAt) {
			return all[i].createdAt.Before(all[j].createdAt)
		}
		return all[i].id < all[j].id
	})
	out := make([]proto.Session, len(all))
	for i, s := range all {
		out[i] = s.Snapshot()
	}
	return out
}

// Remove deletes an exited session and its scrollback.
func (m *Manager) Remove(id string) error {
	s, err := m.Get(id)
	if err != nil {
		return err
	}
	if s.running() {
		return proto.Errorf(proto.CodeSessionRunning, "session is still running; close it first")
	}
	m.remove(s)
	return nil
}

func (m *Manager) remove(s *Session) {
	m.mu.Lock()
	if m.sessions[s.id] != s {
		m.mu.Unlock()
		return
	}
	delete(m.sessions, s.id)
	m.mu.Unlock()
	s.end(EndRemoved)
	m.bus.Publish(proto.Event{Type: proto.EvSessionRemoved, SessionID: s.id})
	m.log.Info("session removed", "session", s.id)
}

// onExit enforces the cap on retained exited sessions.
func (m *Manager) onExit() {
	m.Sweep()
}

// Sweep removes exited sessions past their TTL, and the oldest exited
// sessions beyond MaxExited.
func (m *Manager) Sweep() {
	type exited struct {
		s  *Session
		at time.Time
	}
	m.mu.Lock()
	var list []exited
	for _, s := range m.sessions {
		if at, ok := s.exitedAt(); ok {
			list = append(list, exited{s, at})
		}
	}
	m.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].at.Before(list[j].at) })
	now := m.now()
	for i, e := range list {
		if now.Sub(e.at) > m.cfg.ExitedTTL || len(list)-i > m.cfg.MaxExited {
			m.remove(e.s)
		}
	}
}

// Run sweeps periodically until ctx is done.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Sweep()
		}
	}
}

// Shutdown hangs up every running session, waits for them up to ctx, then
// kills whatever is left.
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	m.closed = true
	var live []*Session
	for _, s := range m.sessions {
		if s.running() {
			live = append(live, s)
		}
	}
	m.mu.Unlock()
	for _, s := range live {
		s.mu.Lock()
		if s.closing == "" {
			s.closing = "agent"
		}
		s.mu.Unlock()
		s.proc.Hangup()
	}
	for _, s := range live {
		select {
		case <-s.exitedCh:
		case <-ctx.Done():
			s.proc.Kill()
			select {
			case <-s.exitedCh:
			case <-time.After(time.Second):
			}
		}
	}
}
