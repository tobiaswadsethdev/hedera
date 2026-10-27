package hooks

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tobiaswadsethdev/hedera/agent/internal/config"
)

// SettingsPath is Claude Code's user settings file:
// $CLAUDE_CONFIG_DIR/settings.json, else ~/.claude/settings.json.
func SettingsPath() (string, error) {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "settings.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// Plan is the result of editing a settings file in memory.
type Plan struct {
	Path     string
	Exists   bool
	Original []byte
	Before   []byte // Original, normalized
	After    []byte
	Disabled bool // disableAllHooks is set
}

func (p *Plan) Changed() bool { return !bytes.Equal(p.Before, p.After) }

// Prepare loads path and applies edit to it. A file that is not valid JSON
// is an error: hedera never rewrites what it cannot parse.
func Prepare(path string, edit func(*Value) error) (*Plan, error) {
	p := &Plan{Path: path}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		p.Exists = true
		p.Original = data
	}
	root := NewObject()
	if len(bytes.TrimSpace(data)) > 0 {
		if root, err = Parse(data); err != nil {
			return nil, fmt.Errorf("%s is not valid JSON (%v); fix it first, hedera will not overwrite it", path, err)
		}
	}
	if p.Exists {
		p.Before = Marshal(root)
	}
	if err := edit(root); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	p.After = Marshal(root)
	if !p.Exists && isEmptyObject(root) {
		p.After = nil
	}
	p.Disabled = HooksDisabled(root)
	return p, nil
}

// isEmptyObject reports whether root is an empty object, i.e. there is
// nothing worth creating a new file for.
func isEmptyObject(root *Value) bool { return root.Obj != nil && len(root.Obj.Keys) == 0 }

// Apply writes the plan: a timestamped backup of the original first, then
// the new content via an atomic rename, keeping the file's permissions.
// It returns the backup path ("" if there was no original).
func (p *Plan) Apply(now time.Time) (string, error) {
	if !p.Changed() || p.After == nil {
		return "", nil
	}
	perm := os.FileMode(0o600)
	backup := ""
	if p.Exists {
		if fi, err := os.Stat(p.Path); err == nil {
			perm = fi.Mode().Perm()
		}
		backup = p.Path + ".hedera-backup-" + now.Format("20060102-150405")
		if err := os.WriteFile(backup, p.Original, 0o600); err != nil {
			return "", fmt.Errorf("writing backup: %w", err)
		}
	} else if err := os.MkdirAll(filepath.Dir(p.Path), 0o700); err != nil {
		return "", err
	}
	return backup, config.WriteFileAtomic(p.Path, p.After, perm)
}

// Diff is a line diff of the normalized file before and after, for
// --dry-run.
func (p *Plan) Diff() string {
	return lineDiff(splitLines(p.Before), splitLines(p.After))
}

func splitLines(b []byte) []string {
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// lineDiff renders a minimal edit script ("-"/"+"/" " prefixes) from an LCS
// table; settings files are small, so O(n·m) is fine.
func lineDiff(a, b []string) string {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out strings.Builder
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			out.WriteString("  " + a[i] + "\n")
			i++
			j++
		case j < m && (i == n || lcs[i][j+1] >= lcs[i+1][j]):
			out.WriteString("+ " + b[j] + "\n")
			j++
		default:
			out.WriteString("- " + a[i] + "\n")
			i++
		}
	}
	return out.String()
}
