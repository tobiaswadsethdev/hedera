package transcript

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
)

const (
	// MaxEntries is how many entries a transcript keeps; older ones are
	// dropped and first_seq moves on.
	MaxEntries = 20000
	// maxLine bounds one JSONL record. Longer records (Claude Code stores
	// whole API requests in some) are skipped.
	maxLine = 16 << 20
	// readChunk is how much one Refresh reads at most, so a long transcript
	// loads in steps rather than in one allocation.
	readChunk = 4 << 20
)

// Transcript is one session's transcript: the files Claude Code wrote it to,
// in order, and the entries read from them so far. Only the last file grows.
type Transcript struct {
	roots []string // where followed paths must be
	real  []string // roots and their resolved paths, for opened files

	mu      sync.Mutex
	files   []*file
	entries []proto.TranscriptEntry // entries[i].Seq == first+i
	first   uint64
	next    uint64
	changed chan struct{}
}

type file struct {
	path    string
	off     int64
	partial []byte
	skip    bool // in the middle of a record longer than maxLine
}

// New returns an empty transcript that accepts files under roots (see
// Roots).
func New(roots []string) *Transcript {
	return &Transcript{roots: roots, real: resolve(roots), first: 1, next: 1, changed: make(chan struct{})}
}

// resolve returns roots plus, for each that is reached through a symlink,
// its real path, which is what a file's resolved path is compared with.
func resolve(roots []string) []string {
	out := append([]string(nil), roots...)
	for _, r := range roots {
		if real, err := filepath.EvalSymlinks(r); err == nil && real != r {
			out = append(out, real)
		}
	}
	return out
}

// Roots returns the directories transcripts may live in: projects/ under
// $CLAUDE_CONFIG_DIR, and under ~/.claude.
func Roots(home string) []string {
	var dirs []string
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		dirs = append(dirs, d)
	}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".claude"))
	}
	var roots []string
	for _, d := range dirs {
		roots = append(roots, filepath.Join(d, "projects"))
	}
	return roots
}

// Allowed reports whether path is a .jsonl file inside one of roots. The
// file may not exist yet: Claude Code creates it with the first message.
func Allowed(path string, roots []string) bool {
	if !filepath.IsAbs(path) || filepath.Ext(path) != ".jsonl" || filepath.Clean(path) != path {
		return false
	}
	return under(path, roots)
}

func under(path string, roots []string) bool {
	for _, r := range roots {
		if rel, err := filepath.Rel(r, path); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return true
		}
	}
	return false
}

// Follow makes path the file new entries come from. A different file than the
// current one is read to its end first, and when reason is set (clear,
// resume, fork) a divider marks the switch. It returns false if path is not
// allowed.
func (t *Transcript) Follow(path, reason string) bool {
	if !Allowed(path, t.roots) {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if n := len(t.files); n > 0 {
		if t.files[n-1].path == path {
			return true
		}
		t.readLocked()
		if reason != "" {
			t.appendLocked(proto.TranscriptEntry{Kind: proto.EntryDivider, Reason: reason})
		}
		for i, f := range t.files {
			if f.path == path {
				// Back to an earlier file (resuming a conversation this PTY
				// already had): it continues from where it was read to.
				t.files = append(append(t.files[:i:i], t.files[i+1:]...), f)
				return true
			}
		}
	}
	t.files = append(t.files, &file{path: path})
	return true
}

// Ready reports whether a transcript file is known.
func (t *Transcript) Ready() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.files) > 0
}

// Refresh reads what Claude Code appended since the last call.
func (t *Transcript) Refresh() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.readLocked()
}

func (t *Transcript) readLocked() {
	if len(t.files) == 0 {
		return
	}
	f := t.files[len(t.files)-1]
	for {
		n, err := t.readChunkLocked(f)
		if err != nil || n < readChunk {
			return
		}
	}
}

func (t *Transcript) readChunkLocked(f *file) (int, error) {
	// The path was checked when it was followed; check where it really points
	// now that the file exists.
	real, err := filepath.EvalSymlinks(f.path)
	if err != nil {
		return 0, err
	}
	if !under(real, t.real) {
		return 0, errors.New("transcript path leaves the Claude projects directory")
	}
	h, err := os.Open(real)
	if err != nil {
		return 0, err
	}
	defer h.Close()
	if st, err := h.Stat(); err != nil || !st.Mode().IsRegular() {
		return 0, errors.New("transcript is not a regular file")
	} else if st.Size() < f.off {
		// Rewritten from scratch; there is no way to line it up again.
		return 0, errors.New("transcript shrank")
	}
	buf := make([]byte, readChunk)
	n, err := h.ReadAt(buf, f.off)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	f.off += int64(n)
	data := buf[:n]
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			if !f.skip {
				f.partial = append(f.partial, data...)
				if len(f.partial) > maxLine {
					f.partial, f.skip = nil, true
				}
			}
			break
		}
		line := data[:i]
		data = data[i+1:]
		if f.skip {
			f.skip = false
			continue
		}
		if len(f.partial) > 0 {
			line = append(f.partial, line...)
			f.partial = nil
		}
		for _, e := range Normalize(line) {
			t.appendLocked(e)
		}
	}
	return n, nil
}

func (t *Transcript) appendLocked(e proto.TranscriptEntry) {
	e.Seq = t.next
	t.next++
	t.entries = append(t.entries, e)
	if over := len(t.entries) - MaxEntries; over > 0 {
		t.entries = append([]proto.TranscriptEntry(nil), t.entries[over:]...)
		t.first += uint64(over)
	}
	close(t.changed)
	t.changed = make(chan struct{})
}

// Changed is closed the next time an entry is added.
func (t *Transcript) Changed() <-chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.changed
}

// Seqs returns the oldest entry held and the newest (0 when empty).
func (t *Transcript) Seqs() (first, last uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.first, t.next - 1
}

// After returns up to n entries after seq.
func (t *Transcript) After(seq uint64, n int) []proto.TranscriptEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	from := max(seq+1, t.first)
	if from >= t.next {
		return nil
	}
	i := int(from - t.first)
	return clone(t.entries[i:min(len(t.entries), i+n)])
}

// Tail returns the last n entries.
func (t *Transcript) Tail(n int) []proto.TranscriptEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	return clone(t.entries[max(0, len(t.entries)-n):])
}

// Before returns up to limit entries before seq, oldest first.
func (t *Transcript) Before(seq uint64, limit int) []proto.TranscriptEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	if seq <= t.first {
		return nil
	}
	end := int(min(seq, t.next) - t.first)
	return clone(t.entries[max(0, end-limit):end])
}

func clone(e []proto.TranscriptEntry) []proto.TranscriptEntry {
	return append([]proto.TranscriptEntry(nil), e...)
}
