package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/tobiaswadsethdev/hedera/agent/internal/config"
	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
)

// The agent owns its PTYs, so restarting it ends every session. Claude Code
// keeps its conversations on disk, though, so a Claude session that was live
// when the agent stopped can be picked up again with `claude --resume`. The
// resume store remembers those sessions across restarts.

const (
	maxResumable = 50
	resumableTTL = 7 * 24 * time.Hour
)

var claudeSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

type resumeRecord struct {
	PtyID           string    `json:"pty_id"`
	ClaudeSessionID string    `json:"claude_session_id"`
	Cwd             string    `json:"cwd"`
	Title           string    `json:"title"`
	Claude          string    `json:"claude"` // the claude command to run
	UpdatedAt       time.Time `json:"updated_at"`
}

type resumeStore struct {
	path string
	now  func() time.Time

	mu   sync.Mutex
	live map[string]resumeRecord // by PTY ID, sessions of this agent run
	lost map[string]resumeRecord // by Claude session ID, from earlier runs
}

func openResumeStore(path string, now func() time.Time) (*resumeStore, error) {
	r := &resumeStore{path: path, now: now, live: map[string]resumeRecord{}, lost: map[string]resumeRecord{}}
	if path == "" {
		return r, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	var recs []resumeRecord
	if err := json.Unmarshal(b, &recs); err != nil {
		return nil, err
	}
	// Everything on disk belongs to an agent run that has ended.
	for _, rec := range recs {
		if now().Sub(rec.UpdatedAt) < resumableTTL && claudeSessionIDPattern.MatchString(rec.ClaudeSessionID) {
			if old, ok := r.lost[rec.ClaudeSessionID]; !ok || rec.UpdatedAt.After(old.UpdatedAt) {
				r.lost[rec.ClaudeSessionID] = rec
			}
		}
	}
	r.saveLocked()
	return r, nil
}

func (r *resumeStore) saveLocked() {
	if r.path == "" {
		return
	}
	recs := make([]resumeRecord, 0, len(r.live)+len(r.lost))
	for _, rec := range r.lost {
		recs = append(recs, rec)
	}
	for _, rec := range r.live {
		recs = append(recs, rec)
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].UpdatedAt.After(recs[j].UpdatedAt) })
	if len(recs) > maxResumable {
		recs = recs[:maxResumable]
	}
	b, _ := json.MarshalIndent(recs, "", "  ")
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err == nil {
		config.WriteFileAtomic(r.path, append(b, '\n'), 0o600)
	}
}

// track records (or refreshes) a session that is running Claude.
func (r *resumeStore) track(rec resumeRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.live[rec.PtyID]; ok && old.ClaudeSessionID == rec.ClaudeSessionID && old.Title == rec.Title {
		return
	}
	rec.UpdatedAt = r.now()
	r.live[rec.PtyID] = rec
	// It is running again, so it is no longer lost.
	delete(r.lost, rec.ClaudeSessionID)
	r.saveLocked()
}

// untrack forgets a session whose Claude ended normally.
func (r *resumeStore) untrack(ptyID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.live[ptyID]; ok {
		delete(r.live, ptyID)
		r.saveLocked()
	}
}

func (r *resumeStore) list() []proto.Resumable {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []proto.Resumable{}
	for _, rec := range r.lost {
		out = append(out, proto.Resumable{
			ClaudeSessionID: rec.ClaudeSessionID,
			Cwd:             rec.Cwd,
			Title:           rec.Title,
			LastSeenAt:      proto.FormatTime(rec.UpdatedAt),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeenAt > out[j].LastSeenAt })
	return out
}

func (r *resumeStore) take(claudeSessionID string) (resumeRecord, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.lost[claudeSessionID]
	return rec, ok
}

func (r *resumeStore) forget(claudeSessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.lost[claudeSessionID]; !ok {
		return false
	}
	delete(r.lost, claudeSessionID)
	r.saveLocked()
	return true
}

// Resumable lists Claude sessions lost to an earlier agent stop.
func (m *Manager) Resumable() []proto.Resumable {
	return m.resume.list()
}

// Resume starts `claude --resume` for a lost session, in its old directory
// and with its old title.
func (m *Manager) Resume(claudeSessionID string, cols, rows int) (*Session, error) {
	rec, ok := m.resume.take(claudeSessionID)
	if !ok {
		return nil, proto.Errorf(proto.CodeNotFound, "no resumable Claude session %q", claudeSessionID)
	}
	s, err := m.Create(proto.CreateRequest{
		Kind:  proto.KindClaude,
		Cwd:   rec.Cwd,
		Argv:  []string{rec.Claude, "--resume", rec.ClaudeSessionID},
		Cols:  cols,
		Rows:  rows,
		Title: rec.Title,
	})
	if err != nil {
		return nil, err
	}
	m.resume.forget(claudeSessionID)
	return s, nil
}

// Forget drops a lost session from the resumable list.
func (m *Manager) Forget(claudeSessionID string) error {
	if !m.resume.forget(claudeSessionID) {
		return proto.Errorf(proto.CodeNotFound, "no resumable Claude session %q", claudeSessionID)
	}
	return nil
}

// trackClaudeLocked updates the resume store after a hook. Called with s.mu
// held; the store has its own lock and never calls back into sessions.
func (s *Session) trackClaudeLocked() {
	st := s.claude.state
	switch {
	case st.Status == proto.ClaudeEnded:
		s.m.resume.untrack(s.id)
	case st.SessionID != nil && claudeSessionIDPattern.MatchString(*st.SessionID):
		claude := "claude"
		if s.kind == proto.KindClaude && len(s.argv) > 0 && filepath.Base(s.argv[0]) == "claude" {
			claude = s.argv[0]
		}
		s.m.resume.track(resumeRecord{PtyID: s.id, ClaudeSessionID: *st.SessionID, Cwd: s.cwd, Title: s.title, Claude: claude})
	}
}
