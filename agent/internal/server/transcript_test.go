//go:build !windows

package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/tobiaswadsethdev/hedera/agent/internal/client"
	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
	"github.com/tobiaswadsethdev/hedera/agent/internal/session"
)

func appendJSONL(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		fmt.Fprintln(f, l)
	}
}

func promptLine(text string) string {
	b, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"content": text}})
	return string(b)
}

// nextTranscript returns the next transcript message's entries.
func nextTranscript(t *testing.T, c *client.Client) proto.Transcript {
	t.Helper()
	for {
		m := next(t, c)
		if m.T != proto.TTranscript {
			continue
		}
		var tr proto.Transcript
		if err := json.Unmarshal(m.Raw, &tr); err != nil {
			t.Fatal(err)
		}
		return tr
	}
}

func TestTranscript(t *testing.T) {
	root := t.TempDir()
	h := newHarness(t, func(c *session.Config) { c.TranscriptRoots = []string{root} })
	s, err := h.mgr.Create(proto.CreateRequest{Kind: proto.KindClaude, Argv: []string{"/bin/sh", "-c", "read x"}})
	if err != nil {
		t.Fatal(err)
	}
	c := h.dialLocal(t)

	var res proto.TranscriptSubscribeResult
	err = c.Call(ctx(t), proto.TTranscriptSubscribe, proto.TranscriptSubscribeRequest{SessionID: s.ID()}, &res)
	if codeOf(err) != proto.CodeUnavailable {
		t.Fatalf("before any hook: %v", err)
	}

	os.MkdirAll(filepath.Join(root, "p"), 0o700)
	path := filepath.Join(root, "p", "abc.jsonl")
	for i := 1; i <= 5; i++ {
		appendJSONL(t, path, promptLine(fmt.Sprintf("prompt %d", i)))
	}
	body, _ := json.Marshal(map[string]string{"hook_event_name": "SessionStart", "session_id": "abc", "source": "startup", "transcript_path": path})
	if code := postHook(t, h, s.ID(), s.HookToken(), string(body)); code != 204 {
		t.Fatalf("hook status %d", code)
	}
	if !s.Snapshot().Claude.Transcript {
		t.Fatal("claude.transcript not set")
	}

	// Without since: the last tail entries, then live ones.
	tail := 2
	if err := c.Call(ctx(t), proto.TTranscriptSubscribe, proto.TranscriptSubscribeRequest{SessionID: s.ID(), Tail: &tail}, &res); err != nil {
		t.Fatal(err)
	}
	if res.Seq != 5 || res.FirstSeq != 1 || res.Reset {
		t.Fatalf("result %+v", res)
	}
	got := nextTranscript(t, c)
	if len(got.Entries) != 2 || got.Entries[0].Seq != 4 || *got.Entries[1].Blocks[0].Text != "prompt 5" {
		t.Fatalf("backlog %+v", got.Entries)
	}
	appendJSONL(t, path, promptLine("prompt 6"))
	if got = nextTranscript(t, c); len(got.Entries) != 1 || got.Entries[0].Seq != 6 {
		t.Fatalf("live %+v", got.Entries)
	}

	// Scrolling back.
	var read proto.TranscriptReadResult
	limit := 10
	if err := c.Call(ctx(t), proto.TTranscriptRead, proto.TranscriptReadRequest{SessionID: s.ID(), Before: 4, Limit: &limit}, &read); err != nil {
		t.Fatal(err)
	}
	if len(read.Entries) != 3 || read.Entries[0].Seq != 1 || read.Entries[2].Seq != 3 {
		t.Fatalf("read %+v", read.Entries)
	}

	// Resubscribing from a known seq replays what came after it.
	since := uint64(4)
	if err := c.Call(ctx(t), proto.TTranscriptSubscribe, proto.TranscriptSubscribeRequest{SessionID: s.ID(), Since: &since}, &res); err != nil {
		t.Fatal(err)
	}
	if got = nextTranscript(t, c); len(got.Entries) != 2 || got.Entries[0].Seq != 5 {
		t.Fatalf("replay %+v", got.Entries)
	}

	// A seq the agent never handed out: reset, nothing follows.
	since = 99
	if err := c.Call(ctx(t), proto.TTranscriptSubscribe, proto.TranscriptSubscribeRequest{SessionID: s.ID(), Since: &since}, &res); err != nil || !res.Reset {
		t.Fatalf("reset: %+v %v", res, err)
	}
	appendJSONL(t, path, promptLine("prompt 7"))
	var probe proto.ListResult
	c.Call(ctx(t), proto.TSessionList, nil, &probe) // let a stray transcript message, if any, arrive first
	select {
	case m := <-c.Messages:
		if m.T == proto.TTranscript {
			t.Fatal("transcript sent after a reset")
		}
	default:
	}

	if err := c.Call(ctx(t), proto.TTranscriptUnsubscribe, proto.TranscriptUnsubscribeRequest{SessionID: s.ID()}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(ctx(t), proto.TTranscriptRead, proto.TranscriptReadRequest{SessionID: "s_aaaaaaaaaa", Before: 1}, &read); codeOf(err) != proto.CodeNotFound {
		t.Fatalf("unknown session: %v", err)
	}
}

func TestTranscriptBatchesLargeBacklogs(t *testing.T) {
	root := t.TempDir()
	h := newHarness(t, func(c *session.Config) { c.TranscriptRoots = []string{root} })
	s, _ := h.mgr.Create(proto.CreateRequest{Kind: proto.KindClaude, Argv: []string{"/bin/sh", "-c", "read x"}})
	os.MkdirAll(filepath.Join(root, "p"), 0o700)
	path := filepath.Join(root, "p", "abc.jsonl")
	big := make([]byte, 15<<10)
	for i := range big {
		big[i] = 'x'
	}
	for range 60 { // ~900 KiB
		appendJSONL(t, path, promptLine(string(big)))
	}
	body, _ := json.Marshal(map[string]string{"hook_event_name": "SessionStart", "session_id": "abc", "transcript_path": path})
	postHook(t, h, s.ID(), s.HookToken(), string(body))

	c := h.dialLocal(t)
	var res proto.TranscriptSubscribeResult
	since := uint64(0)
	if err := c.Call(ctx(t), proto.TTranscriptSubscribe, proto.TranscriptSubscribeRequest{SessionID: s.ID(), Since: &since}, &res); err != nil {
		t.Fatal(err)
	}
	total, messages := 0, 0
	for total < 60 {
		got := nextTranscript(t, c)
		total += len(got.Entries)
		messages++
	}
	if messages < 4 {
		t.Fatalf("%d entries came in %d messages; want batches under 256 KiB", total, messages)
	}
}
