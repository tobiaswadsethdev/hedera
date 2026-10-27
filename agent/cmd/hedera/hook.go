package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

const hookTimeout = time.Second

// cmdHook is the Claude Code hook handler that install-hooks registers. It
// forwards the hook payload on stdin to the agent that owns this PTY.
//
// It must never influence Claude Code: it prints nothing (stdout from some
// hooks becomes model context) and always exits 0 (exit 2 would block).
// Outside a hedera session it does nothing.
func cmdHook([]string) error {
	defer func() { recover() }()
	forwardHook(os.Getenv, os.Stdin, &http.Client{Timeout: hookTimeout})
	return nil
}

func forwardHook(getenv func(string) string, stdin io.Reader, hc *http.Client) {
	body, _ := io.ReadAll(io.LimitReader(stdin, 1<<20))
	id, tok, target := getenv("HEDERA_PTY_ID"), getenv("HEDERA_HOOK_TOKEN"), getenv("HEDERA_HOOK_URL")
	if id == "" || tok == "" || !loopbackHTTP(target) || len(body) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Hedera-Session", id)
	resp, err := hc.Do(req)
	if err != nil {
		return
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

// loopbackHTTP accepts only http://127.0.0.1:port/… style URLs, so the hook
// token is never sent off the machine even if the environment is tampered
// with.
func loopbackHTTP(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return ip != nil && ip.IsLoopback() && u.Port() != ""
}
