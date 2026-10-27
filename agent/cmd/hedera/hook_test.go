//go:build !windows

package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
	"github.com/tobiaswadsethdev/hedera/agent/internal/server"
	"github.com/tobiaswadsethdev/hedera/agent/internal/session"
)

func envMap(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// TestHookReachesAgent runs the hook forwarder against a real agent handler.
func TestHookReachesAgent(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := session.NewManager(session.Config{
		MachineID: "m_testtesttesttest",
		Resolve:   func(_ string, argv []string) ([]string, []string) { return argv, argv },
		BaseEnv:   func() []string { return []string{"PATH=" + os.Getenv("PATH")} },
		Home:      t.TempDir(),
		Logger:    quiet,
	})
	srv := &server.Server{Manager: mgr, LocalToken: "hlt_x", Log: quiet}
	ts := httptest.NewServer(srv.LocalHandler())
	defer ts.Close()
	s, err := mgr.Create(proto.CreateRequest{Kind: proto.KindClaude, Argv: []string{"/bin/sh", "-c", "read x"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close("kill")
	env := envMap(map[string]string{
		"HEDERA_PTY_ID":     s.ID(),
		"HEDERA_HOOK_TOKEN": s.HookToken(),
		"HEDERA_HOOK_URL":   ts.URL + "/v1/hook",
	})
	payload := `{"session_id":"abc","hook_event_name":"UserPromptSubmit","prompt":"hi"}`
	forwardHook(env, strings.NewReader(payload), &http.Client{Timeout: time.Second})
	if snap := s.Snapshot(); snap.Status != proto.StatusWorking || snap.Claude.Turn != 1 {
		t.Fatalf("hook not applied: %s %+v", snap.Status, snap.Claude)
	}
}

func TestHookStaysLocalAndQuiet(t *testing.T) {
	var hits int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer ts.Close()
	hc := &http.Client{Timeout: time.Second}
	base := map[string]string{"HEDERA_PTY_ID": "s_aaaaaaaaaa", "HEDERA_HOOK_TOKEN": "hht_x", "HEDERA_HOOK_URL": ts.URL + "/v1/hook"}
	with := func(k, v string) func(string) string {
		m := map[string]string{}
		for kk, vv := range base {
			m[kk] = vv
		}
		m[k] = v
		return envMap(m)
	}
	for name, env := range map[string]func(string) string{
		"outside hedera": with("HEDERA_PTY_ID", ""),
		"no token":       with("HEDERA_HOOK_TOKEN", ""),
		"remote url":     with("HEDERA_HOOK_URL", "http://100.64.0.1:7374/v1/hook"),
		"https url":      with("HEDERA_HOOK_URL", "https://127.0.0.1:7374/v1/hook"),
		"hostname url":   with("HEDERA_HOOK_URL", "http://localhost.evil.com:7374/v1/hook"),
		"userinfo url":   with("HEDERA_HOOK_URL", "http://a:b@127.0.0.1:7374/v1/hook"),
	} {
		forwardHook(env, strings.NewReader(`{"hook_event_name":"Stop"}`), hc)
		if hits != 0 {
			t.Fatalf("%s: request sent", name)
		}
	}
	forwardHook(envMap(base), strings.NewReader(`{"hook_event_name":"Stop"}`), hc)
	if hits != 1 {
		t.Fatal("valid environment did not forward")
	}
	// An agent that is down must not stall Claude Code for long.
	start := time.Now()
	forwardHook(with("HEDERA_HOOK_URL", "http://127.0.0.1:1/v1/hook"), strings.NewReader(`{}`), hc)
	if time.Since(start) > 2*time.Second {
		t.Fatal("hook blocked too long")
	}
}
