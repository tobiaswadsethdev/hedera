package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
)

// Records in the shapes Claude Code writes, one per line.
var sample = []string{
	`{"type":"queue-operation","operation":"enqueue"}`,
	`{"type":"user","uuid":"u1","timestamp":"2026-10-28T12:05:01.000Z","message":{"role":"user","content":"Run the tests"}}`,
	`{"type":"user","uuid":"m1","isMeta":true,"message":{"role":"user","content":"<local-command-caveat>…</local-command-caveat>"}}`,
	`{"type":"assistant","uuid":"a1","timestamp":"2026-10-28T12:05:02.000Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"x"}]}}`,
	`{"type":"assistant","uuid":"a2","timestamp":"2026-10-28T12:05:03.000Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Run the whole suite.","signature":"x"},{"type":"text","text":"I'll run them."}]}}`,
	`{"type":"assistant","uuid":"a3","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test ./...","description":"Run tests"}}]}}`,
	`{"type":"user","uuid":"r1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","is_error":true,"content":"FAIL"}]}}`,
	`{"type":"user","uuid":"r2","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_2","content":[{"type":"text","text":"line 1"},{"type":"image","source":{}},{"type":"text","text":"line 2"}]}]}}`,
	`{"type":"assistant","uuid":"s1","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"subagent"}]}}`,
	`{"type":"user","uuid":"h1","message":{"role":"user","content":"<task-notification><task-id>x</task-id></task-notification>"}}`,
	`{"type":"user","uuid":"c1","message":{"role":"user","content":"<command-name>/compact</command-name>\n<command-message>compact</command-message>\n<command-args>focus on the parser</command-args>"}}`,
	`{"type":"user","uuid":"o1","message":{"role":"user","content":"<local-command-stdout>Compacted</local-command-stdout>"}}`,
	`{"type":"system","subtype":"compact_boundary","uuid":"b1","content":"Conversation compacted","level":"info"}`,
	`{"type":"user","uuid":"cs","isCompactSummary":true,"message":{"role":"user","content":"This session is being continued…"}}`,
	`{"type":"system","subtype":"api_error","uuid":"e1","level":"error","error":{"formatted":"529 Overloaded"},"retryInMs":4000,"retryAttempt":1,"maxRetries":10}`,
	`{"type":"system","subtype":"turn_duration","uuid":"d1","isMeta":true,"durationMs":30000}`,
	`{"type":"system","subtype":"stop_hook_summary","uuid":"x1"}`,
	`{"type":"user","uuid":"i1","message":{"role":"user","content":[{"type":"text","text":"what's in this?"},{"type":"image","source":{}}]}}`,
	`not json`,
}

func entriesJSON(t *testing.T, es []proto.TranscriptEntry) string {
	t.Helper()
	var lines []string
	for _, e := range es {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(b))
	}
	return strings.Join(lines, "\n")
}

func TestNormalize(t *testing.T) {
	var got []proto.TranscriptEntry
	for _, l := range sample {
		got = append(got, Normalize([]byte(l))...)
	}
	want := strings.Join([]string{
		`{"seq":0,"uuid":"u1","ts":"2026-10-28T12:05:01.000Z","kind":"prompt","blocks":[{"type":"text","text":"Run the tests"}]}`,
		`{"seq":0,"uuid":"a2","ts":"2026-10-28T12:05:03.000Z","kind":"assistant","blocks":[{"type":"thinking","text":"Run the whole suite."},{"type":"text","text":"I'll run them."}]}`,
		`{"seq":0,"uuid":"a3","kind":"assistant","blocks":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test ./...","description":"Run tests"}}]}`,
		`{"seq":0,"uuid":"r1","kind":"tool_result","blocks":[{"type":"tool_result","text":"FAIL","tool_use_id":"toolu_1","is_error":true}]}`,
		`{"seq":0,"uuid":"r2","kind":"tool_result","blocks":[{"type":"tool_result","text":"line 1\nline 2","tool_use_id":"toolu_2","is_error":false,"images":1}]}`,
		`{"seq":0,"uuid":"c1","kind":"prompt","blocks":[{"type":"text","text":"/compact focus on the parser"}]}`,
		`{"seq":0,"uuid":"o1","kind":"notice","blocks":[{"type":"text","text":"Compacted"}],"level":"info"}`,
		`{"seq":0,"uuid":"b1","kind":"divider","reason":"compact"}`,
		`{"seq":0,"uuid":"e1","kind":"notice","blocks":[{"type":"text","text":"API error (529 Overloaded); retrying in 4 s (attempt 1 of 10)"}],"level":"error"}`,
		`{"seq":0,"uuid":"d1","kind":"turn_end","duration_ms":30000}`,
		`{"seq":0,"uuid":"i1","kind":"prompt","blocks":[{"type":"text","text":"what's in this?"},{"type":"image"}]}`,
	}, "\n")
	if g := entriesJSON(t, got); g != want {
		t.Fatalf("got\n%s\nwant\n%s", g, want)
	}
}

func TestClip(t *testing.T) {
	long := strings.Repeat("é", MaxText) // two bytes each
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"` + long + `"},` +
		`{"type":"tool_use","id":"t","name":"Write","input":{"file_path":"/a","content":"` + long + `","n":3}}]}}`
	e := Normalize([]byte(line))[0]
	text := *e.Blocks[0].Text
	if !e.Blocks[0].Truncated || len(text) > MaxText || !strings.HasSuffix(text, "é") {
		t.Fatalf("text block: truncated=%v len=%d", e.Blocks[0].Truncated, len(text))
	}
	var input map[string]any
	json.Unmarshal(e.Blocks[1].Input, &input)
	if !e.Blocks[1].Truncated || len(input["content"].(string)) > MaxText || input["file_path"] != "/a" || input["n"] != 3.0 {
		t.Fatalf("tool input: truncated=%v %v", e.Blocks[1].Truncated, input["file_path"])
	}
}

func TestAllowed(t *testing.T) {
	roots := []string{"/home/me/.claude/projects"}
	for path, want := range map[string]bool{
		"/home/me/.claude/projects/-src-app/1234.jsonl":           true,
		"/home/me/.claude/projects/x.jsonl":                       true,
		"/home/me/.claude/projects/-src-app/1234.json":            false,
		"/home/me/.claude/projects/../settings.jsonl":             false,
		"/home/me/.claude/other/1234.jsonl":                       false,
		"relative/1234.jsonl":                                     false,
		"/home/me/.claude/projects":                               false,
		"/home/me/.claude/projects-evil/1234.jsonl":               false,
		"/home/me/.claude/projects/-src-app/sub/../../../x.jsonl": false,
	} {
		if got := Allowed(path, roots); got != want {
			t.Errorf("Allowed(%q) = %v", path, got)
		}
	}
}

func setup(t *testing.T) (*Transcript, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "projects")
	os.MkdirAll(filepath.Join(root, "p"), 0o700)
	return New([]string{root}), filepath.Join(root, "p")
}

func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		f.WriteString(l + "\n")
	}
}

const (
	prompt = `{"type":"user","message":{"content":"hello"}}`
	reply  = `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`
)

func kinds(es []proto.TranscriptEntry) string {
	var k []string
	for _, e := range es {
		k = append(k, e.Kind)
	}
	return strings.Join(k, ",")
}

func TestFollowAndRefresh(t *testing.T) {
	tr, dir := setup(t)
	first := filepath.Join(dir, "a.jsonl")
	if !tr.Follow(first, "") {
		t.Fatal("follow refused")
	}
	tr.Refresh() // not created yet: nothing, no error
	if _, last := tr.Seqs(); last != 0 {
		t.Fatalf("last = %d", last)
	}

	changed := tr.Changed()
	appendLines(t, first, prompt)
	// Half a record: kept until its newline arrives.
	f, _ := os.OpenFile(first, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(reply[:20])
	f.Close()
	tr.Refresh()
	select {
	case <-changed:
	default:
		t.Fatal("Changed not closed")
	}
	if got := kinds(tr.After(0, 10)); got != "prompt" {
		t.Fatalf("got %s", got)
	}
	f, _ = os.OpenFile(first, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(reply[20:] + "\n")
	f.Close()
	tr.Refresh()
	if got := kinds(tr.After(0, 10)); got != "prompt,assistant" {
		t.Fatalf("got %s", got)
	}

	// /clear: the old file is read to its end, then a divider, then the new one.
	appendLines(t, first, reply)
	second := filepath.Join(dir, "b.jsonl")
	appendLines(t, second, prompt)
	tr.Follow(second, "clear")
	tr.Refresh()
	all := tr.After(0, 10)
	if got := kinds(all); got != "prompt,assistant,assistant,divider,prompt" {
		t.Fatalf("got %s", got)
	}
	for i, e := range all {
		if e.Seq != uint64(i+1) {
			t.Fatalf("entry %d has seq %d", i, e.Seq)
		}
	}
	if all[3].Reason != "clear" {
		t.Fatalf("divider %+v", all[3])
	}

	// Following the same file again changes nothing; paths outside the
	// roots are refused.
	tr.Follow(second, "resume")
	if tr.Follow("/etc/passwd.jsonl", "") {
		t.Fatal("followed a file outside the roots")
	}
	if _, last := tr.Seqs(); last != 5 {
		t.Fatalf("last = %d", last)
	}

	// Back to the first conversation (claude --resume): it continues there.
	tr.Follow(first, "resume")
	appendLines(t, first, prompt)
	tr.Refresh()
	if got := kinds(tr.After(5, 10)); got != "divider,prompt" {
		t.Fatalf("got %s", got)
	}
}

func TestPaging(t *testing.T) {
	tr, dir := setup(t)
	path := filepath.Join(dir, "a.jsonl")
	for range 10 {
		appendLines(t, path, prompt, reply)
	}
	tr.Follow(path, "")
	tr.Refresh()
	if first, last := tr.Seqs(); first != 1 || last != 20 {
		t.Fatalf("seqs %d %d", first, last)
	}
	tail := tr.Tail(3)
	if len(tail) != 3 || tail[0].Seq != 18 {
		t.Fatalf("tail %+v", tail)
	}
	before := tr.Before(18, 5)
	if len(before) != 5 || before[0].Seq != 13 || before[4].Seq != 17 {
		t.Fatalf("before %+v", before)
	}
	if got := tr.Before(1, 5); len(got) != 0 {
		t.Fatalf("before 1: %+v", got)
	}
	if got := tr.After(20, 5); len(got) != 0 {
		t.Fatalf("after 20: %+v", got)
	}
	if got := tr.After(17, 2); len(got) != 2 || got[0].Seq != 18 {
		t.Fatalf("after 17: %+v", got)
	}
}

func TestSkipsOversizedRecords(t *testing.T) {
	tr, dir := setup(t)
	path := filepath.Join(dir, "a.jsonl")
	huge := `{"type":"api-request-blob","x":"` + strings.Repeat("x", maxLine+10) + `"}`
	appendLines(t, path, prompt, huge, reply)
	tr.Follow(path, "")
	tr.Refresh()
	if got := kinds(tr.After(0, 10)); got != "prompt,assistant" {
		t.Fatalf("got %s", got)
	}
}

func TestRefusesSymlinkOutOfRoots(t *testing.T) {
	tr, dir := setup(t)
	outside := filepath.Join(t.TempDir(), "secret.jsonl")
	appendLines(t, outside, prompt)
	link := filepath.Join(dir, "a.jsonl")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip(err)
	}
	tr.Follow(link, "")
	tr.Refresh()
	if _, last := tr.Seqs(); last != 0 {
		t.Fatal("read through a symlink leaving the roots")
	}
}
