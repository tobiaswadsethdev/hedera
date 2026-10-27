package hooks

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const cmd = "/usr/local/bin/hedera hook"

func mustParse(t *testing.T, s string) *Value {
	t.Helper()
	v, err := Parse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRoundTripPreservesOrderAndSpelling(t *testing.T) {
	in := `{
  "zeta": 1.50,
  "alpha": {"b": [1e3, true, null, "<&>"], "a": "ünïcode"},
  "empty": {},
  "list": []
}`
	got := string(Marshal(mustParse(t, in)))
	want := `{
  "zeta": 1.50,
  "alpha": {
    "b": [
      1e3,
      true,
      null,
      "<&>"
    ],
    "a": "ünïcode"
  },
  "empty": {},
  "list": []
}
`
	if got != want {
		t.Fatalf("got\n%s", got)
	}
	for _, bad := range []string{"{", `{"a":1} x`, `[1,]`, ""} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

const userSettings = `{
  "model": "opus",
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {"type": "command", "command": "afplay /System/Library/Sounds/Glass.aiff"}
        ]
      }
    ],
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "~/bin/guard.sh"}]}
    ]
  },
  "permissions": {"allow": ["Bash(ls:*)"]}
}`

func TestInstallKeepsUserHooks(t *testing.T) {
	root := mustParse(t, userSettings)
	if err := Install(root, cmd); err != nil {
		t.Fatal(err)
	}
	out := string(Marshal(root))
	for _, want := range []string{"afplay /System/Library/Sounds/Glass.aiff", "~/bin/guard.sh", `"model": "opus"`, `"Bash(ls:*)"`} {
		if !strings.Contains(out, want) {
			t.Errorf("lost %q", want)
		}
	}
	// Top-level key order is unchanged.
	if strings.Index(out, `"model"`) > strings.Index(out, `"hooks"`) || strings.Index(out, `"hooks"`) > strings.Index(out, `"permissions"`) {
		t.Fatal("key order changed")
	}
	// The user's Stop hook stays first; ours is appended.
	stop := root.Obj.Get("hooks").Obj.Get("Stop").Arr
	if len(stop) != 2 || IsOurs(stop[0].Obj.Get("hooks").Arr[0]) || !IsOurs(stop[1].Obj.Get("hooks").Arr[0]) {
		t.Fatal("Stop hooks not merged as expected")
	}
	if got := strings.Join(Installed(root), ","); got != "SessionStart,UserPromptSubmit,PermissionRequest,PostToolUse,PostToolUseFailure,Notification,Stop,StopFailure,SessionEnd" {
		t.Fatalf("installed %s", got)
	}
}

func TestInstallIsIdempotentAndUpdatesPath(t *testing.T) {
	root := mustParse(t, userSettings)
	Install(root, cmd)
	first := string(Marshal(root))
	Install(root, cmd)
	if second := string(Marshal(root)); second != first {
		t.Fatalf("second install changed the file:\n%s", lineDiff(strings.Split(first, "\n"), strings.Split(second, "\n")))
	}
	Install(root, "'/Users/t/My Tools/hedera' hook")
	out := string(Marshal(root))
	if strings.Contains(out, cmd) || strings.Count(out, "My Tools/hedera") != len(Registrations) {
		t.Fatalf("path not updated:\n%s", out)
	}
}

func TestHandlerShape(t *testing.T) {
	root := NewObject()
	Install(root, cmd)
	h := root.Obj.Get("hooks").Obj
	stop := string(Marshal(h.Get("Stop").Arr[0]))
	if stop != "{\n  \"hooks\": [\n    {\n      \"type\": \"command\",\n      \"command\": \"/usr/local/bin/hedera hook\",\n      \"timeout\": 5\n    }\n  ]\n}\n" {
		t.Fatalf("Stop handler:\n%s", stop)
	}
	post := h.Get("PostToolUse").Arr[0].Obj.Get("hooks").Arr[0].Obj
	if b, _ := post.Get("async").scalarBool(); !b || post.Get("timeout") != nil {
		t.Fatal("PostToolUse should be async without a timeout")
	}
	end := h.Get("SessionEnd").Arr[0].Obj.Get("hooks").Arr[0].Obj
	if end.Get("timeout") != nil || end.Get("async") != nil {
		t.Fatal("SessionEnd must keep Claude Code's default budget")
	}
}

func TestUninstallRemovesOnlyOurs(t *testing.T) {
	root := mustParse(t, userSettings)
	before := string(Marshal(root))
	Install(root, cmd)
	// A group mixing ours with the user's keeps the user's handler.
	stop := root.Obj.Get("hooks").Obj.Get("Stop")
	mixed := stop.Arr[1].Obj.Get("hooks")
	mixed.Arr = append(mixed.Arr, mustParse(t, `{"type":"command","command":"say done"}`))
	if err := Uninstall(root); err != nil {
		t.Fatal(err)
	}
	out := string(Marshal(root))
	if strings.Contains(out, "hedera") || !strings.Contains(out, "say done") || !strings.Contains(out, "Glass.aiff") {
		t.Fatalf("uninstall result:\n%s", out)
	}
	// Without the extra handler, uninstall restores the original exactly.
	root = mustParse(t, userSettings)
	Install(root, cmd)
	Uninstall(root)
	if got := string(Marshal(root)); got != before {
		t.Fatalf("not restored:\n%s", lineDiff(strings.Split(before, "\n"), strings.Split(got, "\n")))
	}
	// A file that only had our hooks loses the "hooks" key entirely.
	root = NewObject()
	Install(root, cmd)
	Uninstall(root)
	if string(Marshal(root)) != "{}\n" {
		t.Fatalf("got %s", Marshal(root))
	}
}

func TestRefusesUnexpectedShapes(t *testing.T) {
	for _, s := range []string{`[]`, `{"hooks": []}`, `{"hooks": {"Stop": {}}}`} {
		if err := Install(mustParse(t, s), cmd); err == nil {
			t.Errorf("%s accepted", s)
		}
	}
	// Groups we don't understand are left in place.
	root := mustParse(t, `{"hooks": {"Stop": ["weird", {"matcher": "x"}]}}`)
	if err := Install(root, cmd); err != nil {
		t.Fatal(err)
	}
	if n := len(root.Obj.Get("hooks").Obj.Get("Stop").Arr); n != 3 {
		t.Fatalf("%d groups", n)
	}
}

func TestIsOurs(t *testing.T) {
	yes := []string{
		`{"type":"command","command":"/usr/local/bin/hedera hook"}`,
		`{"type":"command","command":"'/Users/t/My Tools/hedera' hook"}`,
		`{"type":"command","command":"\"/Users/t/My Tools/hedera\" hook","timeout":9}`,
		`{"type":"command","command":"/a/b\\ c/hedera hook"}`,
		`{"type":"command","command":"C:\\Tools\\hedera.exe","args":["hook"]}`,
		`{"type":"command","command":"hedera hook"}`,
	}
	no := []string{
		`{"type":"command","command":"/usr/local/bin/hedera hook; rm -rf ~"}`,
		`{"type":"command","command":"/usr/local/bin/hedera run"}`,
		`{"type":"command","command":"/usr/local/bin/hedera-notify hook"}`,
		`{"type":"command","command":"$HOME/bin/hedera hook"}`,
		`{"type":"http","url":"http://127.0.0.1/hedera"}`,
		`{"type":"command","command":"/bin/hedera","args":["hook","x"]}`,
		`"just a string"`,
	}
	for _, s := range yes {
		if !IsOurs(mustParse(t, s)) {
			t.Errorf("not recognised: %s", s)
		}
	}
	for _, s := range no {
		if IsOurs(mustParse(t, s)) {
			t.Errorf("wrongly claimed: %s", s)
		}
	}
}

func TestCommandQuoting(t *testing.T) {
	cases := map[string]string{
		"/usr/local/bin/hedera":    "/usr/local/bin/hedera hook",
		"/Users/t/My Tools/hedera": "'/Users/t/My Tools/hedera' hook",
		"/tmp/it's/hedera":         `'/tmp/it'\''s/hedera' hook`,
	}
	for exe, want := range cases {
		got := Command(exe)
		if got != want {
			t.Errorf("%s: %s", exe, got)
		}
		if w := shellWords(got); len(w) != 2 || w[0] != exe {
			t.Errorf("%s does not parse back: %q", got, w)
		}
	}
}

func TestPrepareAndApply(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")

	// No file yet: install creates it.
	p, err := Prepare(path, func(v *Value) error { return Install(v, cmd) })
	if err != nil || !p.Changed() || p.Exists {
		t.Fatalf("plan %+v %v", p, err)
	}
	if backup, err := p.Apply(time.Now()); err != nil || backup != "" {
		t.Fatalf("apply: %q %v", backup, err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", fi.Mode())
		}
	}

	// Existing user file: backup is written, permissions kept.
	os.WriteFile(path, []byte(userSettings), 0o644)
	os.Chmod(path, 0o644)
	p, err = Prepare(path, func(v *Value) error { return Install(v, cmd) })
	if err != nil {
		t.Fatal(err)
	}
	if d := p.Diff(); !strings.Contains(d, "+ ") || strings.Contains(d, "- ") {
		t.Fatalf("install diff should only add lines:\n%s", d)
	}
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	backup, err := p.Apply(now)
	if err != nil || !strings.HasSuffix(backup, ".hedera-backup-20261027-120000") {
		t.Fatalf("backup %q %v", backup, err)
	}
	if b, _ := os.ReadFile(backup); string(b) != userSettings {
		t.Fatal("backup is not the original")
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o644 {
			t.Fatalf("mode %v", fi.Mode())
		}
	}

	// Re-running changes nothing.
	p, _ = Prepare(path, func(v *Value) error { return Install(v, cmd) })
	if p.Changed() {
		t.Fatalf("second install not a no-op:\n%s", p.Diff())
	}

	// Invalid JSON is never touched.
	os.WriteFile(path, []byte(`{"hooks": `), 0o644)
	if _, err := Prepare(path, func(v *Value) error { return Install(v, cmd) }); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Uninstall with no file does not create one.
	missing := filepath.Join(dir, "none", "settings.json")
	p, _ = Prepare(missing, Uninstall)
	if p.Changed() {
		t.Fatal("uninstall would create a file")
	}
}

func TestHooksDisabled(t *testing.T) {
	if !HooksDisabled(mustParse(t, `{"disableAllHooks": true}`)) || HooksDisabled(mustParse(t, `{"disableAllHooks": "yes"}`)) {
		t.Fatal("disableAllHooks detection")
	}
}

func TestSettingsPath(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/tmp/claude-work")
	if p, _ := SettingsPath(); p != filepath.Join("/tmp/claude-work", "settings.json") {
		t.Fatal(p)
	}
}
