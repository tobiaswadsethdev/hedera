//go:build !windows

package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/tobiaswadsethdev/hedera/agent/internal/client"
	"github.com/tobiaswadsethdev/hedera/agent/internal/pairing"
	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
	"github.com/tobiaswadsethdev/hedera/agent/internal/push"
	"github.com/tobiaswadsethdev/hedera/agent/internal/session"
	"github.com/tobiaswadsethdev/hedera/agent/internal/tlsutil"
)

type harness struct {
	srv       *Server
	mgr       *session.Manager
	local     *httptest.Server
	remote    *httptest.Server
	localTok  string
	fp        string
	localURL  string
	remoteURL string
}

func newHarness(t *testing.T, mod func(*session.Config)) *harness {
	t.Helper()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := session.Config{
		MachineID: "m_testtesttesttest",
		Resolve:   func(_ string, argv []string) ([]string, []string) { return argv, argv },
		BaseEnv:   func() []string { return []string{"PATH=" + os.Getenv("PATH")} },
		Home:      t.TempDir(),
		Logger:    quiet,
	}
	if mod != nil {
		mod(&cfg)
	}
	mgr := session.NewManager(cfg)
	store, err := pairing.Open(filepath.Join(t.TempDir(), "pairings.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tlsutil.LoadOrCreate(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{mgr: mgr, localTok: "hlt_local-secret", fp: tlsutil.Fingerprint(cert.Leaf)}
	h.srv = &Server{
		Manager:            mgr,
		Pairings:           store,
		LocalToken:         h.localTok,
		MachineID:          "m_testtesttesttest",
		BootID:             "b_testtestte",
		Hostname:           "testhost",
		Version:            "test",
		Log:                quiet,
		AllowLoopbackPeers: true,
		PairInfo: func() PairInfo {
			return PairInfo{Addrs: []netip.Addr{netip.MustParseAddr("100.64.0.1")}, Port: 7373, Fingerprint: h.fp}
		},
	}
	h.local = httptest.NewServer(h.srv.LocalHandler())
	h.remote = httptest.NewUnstartedServer(h.srv.RemoteHandler())
	h.remote.TLS = tlsutil.ServerConfig(cert)
	h.remote.StartTLS()
	h.localURL = "ws" + strings.TrimPrefix(h.local.URL, "http") + "/v1/ws"
	h.remoteURL = "wss" + strings.TrimPrefix(h.remote.URL, "https") + "/v1/ws"
	t.Cleanup(func() {
		h.srv.CloseConnections("", websocket.StatusGoingAway, "test over")
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		mgr.Shutdown(ctx)
		h.local.Close()
		h.remote.Close()
	})
	return h
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

func (h *harness) dialLocal(t *testing.T) *client.Client {
	t.Helper()
	c, err := client.Dial(ctx(t), client.Options{URL: h.localURL, Token: h.localTok})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func (h *harness) pair(t *testing.T, code string) (*http.Response, map[string]string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"code": code, "device_name": "test phone"})
	resp, err := client.HTTPClient(h.fp).Post(h.remote.URL+"/v1/pair", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]string
	json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func (h *harness) localAPI(t *testing.T, method, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, h.local.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+h.localTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func codeOf(err error) string {
	var pe *proto.Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

// next returns the next message, failing after a timeout.
func next(t *testing.T, c *client.Client) client.Message {
	t.Helper()
	select {
	case m, ok := <-c.Messages:
		if !ok {
			t.Fatalf("connection closed: %v", c.Err())
		}
		return m
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a message")
	}
	return client.Message{}
}

// readOutputUntil collects OUTPUT bytes on channel ch until substr appears or
// a non-output message arrives (returned).
func readOutputUntil(t *testing.T, c *client.Client, ch uint32, substr string) ([]byte, uint64, *client.Message) {
	t.Helper()
	var out []byte
	var nextOff uint64
	for {
		m := next(t, c)
		if m.Output == nil {
			return out, nextOff, &m
		}
		if m.Output.Channel != ch {
			continue
		}
		out = append(out, m.Output.Payload...)
		nextOff = m.Output.Offset + uint64(len(m.Output.Payload))
		if substr != "" && bytes.Contains(out, []byte(substr)) {
			return out, nextOff, nil
		}
	}
}

func create(t *testing.T, c *client.Client, argv ...string) proto.Session {
	t.Helper()
	var res proto.CreateResult
	if err := c.Call(ctx(t), proto.TSessionCreate, proto.CreateRequest{Kind: proto.KindShell, Argv: argv}, &res); err != nil {
		t.Fatal(err)
	}
	return res.Session
}

func attach(t *testing.T, c *client.Client, id string, from *uint64) proto.AttachResult {
	t.Helper()
	var res proto.AttachResult
	if err := c.Call(ctx(t), proto.TSessionAttach, proto.AttachRequest{SessionID: id, FromOffset: from}, &res); err != nil {
		t.Fatal(err)
	}
	return res
}

func TestLocalAuth(t *testing.T) {
	h := newHarness(t, nil)
	for _, tok := range []string{"", "wrong", h.localTok + "x"} {
		_, err := client.Dial(ctx(t), client.Options{URL: h.localURL, Token: tok})
		if !errors.Is(err, client.ErrUnauthorized) {
			t.Errorf("token %q: %v", tok, err)
		}
	}
	c := h.dialLocal(t)
	if c.Hello.Protocol != 1 || c.Hello.MachineID != "m_testtesttesttest" || c.Hello.BootID == "" {
		t.Fatalf("hello %+v", c.Hello)
	}
	if c.Hello.Limits.MaxFrame != proto.MaxPayload {
		t.Fatalf("limits %+v", c.Hello.Limits)
	}
}

func TestLocalAPIRequiresToken(t *testing.T) {
	h := newHarness(t, nil)
	resp, _ := http.Get(h.local.URL + "/v1/local/pairings")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if resp := h.localAPI(t, "GET", "/v1/local/pairings"); resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestSubprotocolRequired(t *testing.T) {
	h := newHarness(t, nil)
	_, resp, err := websocket.Dial(ctx(t), h.localURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + h.localTok}},
	})
	if err == nil || resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("err %v resp %v", err, resp)
	}
}

func rawDial(t *testing.T, h *harness) *websocket.Conn {
	t.Helper()
	ws, _, err := websocket.Dial(ctx(t), h.localURL, &websocket.DialOptions{
		HTTPHeader:   http.Header{"Authorization": {"Bearer " + h.localTok}},
		Subprotocols: []string{proto.Subprotocol},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.CloseNow() })
	return ws
}

func expectClose(t *testing.T, ws *websocket.Conn, code websocket.StatusCode) {
	t.Helper()
	for {
		_, _, err := ws.Read(ctx(t))
		if err != nil {
			if got := websocket.CloseStatus(err); got != code {
				t.Fatalf("close status %d, want %d (%v)", got, code, err)
			}
			return
		}
	}
}

func TestRequestBeforeHello(t *testing.T) {
	h := newHarness(t, nil)
	ws := rawDial(t, h)
	ws.Write(ctx(t), websocket.MessageText, []byte(`{"t":"session.list","id":1}`))
	expectClose(t, ws, proto.CloseProtocolViolation)
}

func TestHelloTimeout(t *testing.T) {
	old := helloTimeout
	helloTimeout = 100 * time.Millisecond
	t.Cleanup(func() { helloTimeout = old })
	h := newHarness(t, nil)
	expectClose(t, rawDial(t, h), proto.CloseHandshake)
}

func TestUnsupportedVersion(t *testing.T) {
	h := newHarness(t, nil)
	ws := rawDial(t, h)
	ws.Write(ctx(t), websocket.MessageText, []byte(`{"t":"hello","id":1,"protocol":{"min":2,"max":3},"client":{"name":"x","version":"1"}}`))
	_, data, err := ws.Read(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	var r proto.Response
	json.Unmarshal(data, &r)
	if r.OK || r.Error == nil || r.Error.Code != proto.CodeUnsupportedVersion {
		t.Fatalf("response %s", data)
	}
	expectClose(t, ws, proto.CloseHandshake)
}

func TestMalformedMessages(t *testing.T) {
	h := newHarness(t, nil)
	hello := `{"t":"hello","id":1,"protocol":{"min":1,"max":1},"client":{"name":"x","version":"1"}}`
	for name, msg := range map[string]struct {
		typ  websocket.MessageType
		data []byte
	}{
		"not json":         {websocket.MessageText, []byte("{nope")},
		"missing id":       {websocket.MessageText, []byte(`{"t":"session.list"}`)},
		"bad binary":       {websocket.MessageBinary, []byte{0x07, 1, 2}},
		"output to agent":  {websocket.MessageBinary, proto.AppendOutput(nil, 1, 0, []byte("x"))},
		"oversized binary": {websocket.MessageBinary, proto.AppendInput(nil, 1, make([]byte, proto.MaxPayload+1))},
	} {
		t.Run(name, func(t *testing.T) {
			ws := rawDial(t, h)
			ws.Write(ctx(t), websocket.MessageText, []byte(hello))
			ws.Read(ctx(t))
			ws.Write(ctx(t), msg.typ, msg.data)
			expectClose(t, ws, proto.CloseProtocolViolation)
		})
	}
}

func TestErrorsOverTheWire(t *testing.T) {
	h := newHarness(t, nil)
	c := h.dialLocal(t)
	cases := []struct {
		t      string
		params any
		code   string
	}{
		{"session.bogus", nil, proto.CodeUnknownType},
		{proto.TPushConfigure, map[string]string{"radix_url": "https://x", "push_token": "y"}, proto.CodeUnknownType},
		{proto.TSessionAttach, proto.AttachRequest{SessionID: "s_aaaaaaaaaa"}, proto.CodeNotFound},
		{proto.TSessionAttach, map[string]any{"session_id": "s_aaaaaaaaaa", "cols": 10}, proto.CodeInvalidArgument},
		{proto.TSessionClose, proto.CloseRequest{SessionID: "s_aaaaaaaaaa"}, proto.CodeNotFound},
		{proto.TSessionDetach, proto.DetachRequest{Channel: 99}, proto.CodeNotFound},
		{proto.TSessionCreate, map[string]any{"kind": "shell", "cols": "wide"}, proto.CodeInvalidArgument},
		{proto.TSessionInput, proto.InputRequest{SessionID: "s_aaaaaaaaaa", Data: strings.Repeat("x", maxInputLength+1)}, proto.CodeInvalidArgument},
		{proto.THello, proto.Hello{Protocol: proto.VersionRange{Min: 1, Max: 1}}, proto.CodeInvalidArgument},
	}
	for _, tc := range cases {
		if got := codeOf(c.Call(ctx(t), tc.t, tc.params, nil)); got != tc.code {
			t.Errorf("%s: code %q, want %q", tc.t, got, tc.code)
		}
	}
}

func TestShellRoundTrip(t *testing.T) {
	h := newHarness(t, nil)
	c := h.dialLocal(t)
	s := create(t, c, "/bin/sh", "-c", `read x; echo "got:$x"`)
	a := attach(t, c, s.ID, nil)
	if a.Channel == 0 || a.Session.ID != s.ID || a.Session.Attached != 1 {
		t.Fatalf("attach %+v", a)
	}
	if err := c.Input(ctx(t), a.Channel, []byte("hello\r")); err != nil {
		t.Fatal(err)
	}
	if _, _, m := readOutputUntil(t, c, a.Channel, "got:hello"); m != nil {
		t.Fatalf("unexpected message %s", m.Raw)
	}
	// After the process exits, the rest of the output arrives, then detached.
	_, _, m := readOutputUntil(t, c, a.Channel, "")
	var d proto.Detached
	json.Unmarshal(m.Raw, &d)
	if m.T != proto.TDetached || d.Reason != "exited" || d.Channel != a.Channel {
		t.Fatalf("got %s", m.Raw)
	}
	var list proto.ListResult
	if err := c.Call(ctx(t), proto.TSessionList, nil, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Sessions) != 1 || list.Sessions[0].Status != proto.StatusExited || *list.Sessions[0].Exit.Code != 0 {
		t.Fatalf("list %+v", list)
	}
	if err := c.Call(ctx(t), proto.TSessionRemove, proto.RemoveRequest{SessionID: s.ID}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestJSONInputAndResize(t *testing.T) {
	h := newHarness(t, nil)
	c := h.dialLocal(t)
	s := create(t, c, "/bin/sh", "-c", `read x; stty size; echo "x=$x"`)
	a := attach(t, c, s.ID, nil)
	if err := c.Call(ctx(t), proto.TSessionResize, proto.ResizeRequest{SessionID: s.ID, Cols: 132, Rows: 43}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(ctx(t), proto.TSessionInput, proto.InputRequest{SessionID: s.ID, Data: "typed", Submit: true}, nil); err != nil {
		t.Fatal(err)
	}
	out, _, _ := readOutputUntil(t, c, a.Channel, "x=typed")
	if !bytes.Contains(out, []byte("43 132")) {
		t.Fatalf("output %q", out)
	}
}

// TestReconnectResumesFromOffset simulates a phone that drops off and comes
// back: it re-attaches from the offset after the last byte it rendered and
// gets exactly what it missed.
func TestReconnectResumesFromOffset(t *testing.T) {
	h := newHarness(t, nil)
	c1 := h.dialLocal(t)
	s := create(t, c1, "/bin/sh", "-c", `echo one; read x; echo two`)
	a := attach(t, c1, s.ID, nil)
	seen, nextOff, _ := readOutputUntil(t, c1, a.Channel, "one\r\n")
	c1.Close()

	c2 := h.dialLocal(t)
	if err := c2.Call(ctx(t), proto.TSessionInput, proto.InputRequest{SessionID: s.ID, Data: "", Submit: true}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return strings.Contains(sessionOutput(t, h, s.ID), "two") })
	a2 := attach(t, c2, s.ID, &nextOff)
	if a2.Replay.From != nextOff || a2.Replay.Truncated {
		t.Fatalf("replay %+v", a2.Replay)
	}
	missed, _, _ := readOutputUntil(t, c2, a2.Channel, "two")
	if bytes.Contains(missed, []byte("one")) {
		t.Fatalf("replay repeated old output: %q", missed)
	}
	if full := sessionOutput(t, h, s.ID); string(seen)+string(missed) != full[:len(seen)+len(missed)] {
		t.Fatalf("seen+missed %q does not match stream %q", string(seen)+string(missed), full)
	}
}

func sessionOutput(t *testing.T, h *harness, id string) string {
	t.Helper()
	s, err := h.mgr.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	zero := uint64(0)
	sub, _, _ := s.Attach(&zero)
	defer s.Detach(sub)
	var out []byte
	chunks, _ := sub.Take()
	for _, c := range chunks {
		out = append(out, c.Data...)
	}
	return string(out)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatal("condition not met")
}

func TestDetachStopsOutput(t *testing.T) {
	h := newHarness(t, nil)
	c := h.dialLocal(t)
	s := create(t, c, "/bin/sh", "-c", `while :; do echo tick; sleep 0.01; done`)
	a := attach(t, c, s.ID, nil)
	readOutputUntil(t, c, a.Channel, "tick")
	if err := c.Call(ctx(t), proto.TSessionDetach, proto.DetachRequest{Channel: a.Channel}, nil); err != nil {
		t.Fatal(err)
	}
	// Frames sent before the response may still be queued client-side; once
	// they're drained, nothing more may arrive.
	drain := time.After(300 * time.Millisecond)
	var last time.Time
	for done := false; !done; {
		select {
		case m := <-c.Messages:
			if m.Output != nil && m.Output.Channel == a.Channel {
				last = time.Now()
			}
		case <-drain:
			done = true
		}
	}
	if !last.IsZero() && time.Since(last) < 200*time.Millisecond {
		t.Fatal("output kept arriving after detach")
	}
	if snap, _ := h.mgr.Get(s.ID); snap.Snapshot().Attached != 0 {
		t.Fatal("still attached")
	}
}

func TestLaggedChannel(t *testing.T) {
	h := newHarness(t, func(c *session.Config) { c.SubscriberLimit = 1 })
	c := h.dialLocal(t)
	s := create(t, c, "/bin/sh", "-c", `read x; echo "lots of output"`)
	a := attach(t, c, s.ID, nil)
	c.Input(ctx(t), a.Channel, []byte("go\r"))
	_, _, m := readOutputUntil(t, c, a.Channel, "")
	var l proto.Lagged
	json.Unmarshal(m.Raw, &l)
	if m.T != proto.TLagged || l.Channel != a.Channel || l.SessionID != s.ID {
		t.Fatalf("got %s", m.Raw)
	}
}

func TestEvents(t *testing.T) {
	h := newHarness(t, nil)
	c := h.dialLocal(t)
	var sub proto.SubscribeResult
	if err := c.Call(ctx(t), proto.TEventsSubscribe, proto.SubscribeRequest{}, &sub); err != nil {
		t.Fatal(err)
	}
	s := create(t, c, "true")
	var got []proto.Event
	for len(got) < 2 {
		m := next(t, c)
		if m.T != proto.TEvent {
			continue
		}
		var ev proto.Event
		json.Unmarshal(m.Raw, &ev)
		got = append(got, ev)
	}
	if got[0].Type != proto.EvSessionCreated || got[0].Session.ID != s.ID || got[1].Type != proto.EvSessionExited {
		t.Fatalf("events %+v", got)
	}

	// A second client catches up from a sequence number.
	c2 := h.dialLocal(t)
	since := got[0].Seq - 1
	if err := c2.Call(ctx(t), proto.TEventsSubscribe, proto.SubscribeRequest{SinceSeq: &since}, &sub); err != nil {
		t.Fatal(err)
	}
	if sub.Reset {
		t.Fatal("unexpected reset")
	}
	m := next(t, c2)
	var ev proto.Event
	json.Unmarshal(m.Raw, &ev)
	if ev.Seq != got[0].Seq || ev.Type != proto.EvSessionCreated {
		t.Fatalf("backlog %s", m.Raw)
	}

	// Unknown history means reset.
	far := uint64(1 << 40)
	if err := c2.Call(ctx(t), proto.TEventsSubscribe, proto.SubscribeRequest{SinceSeq: &far}, &sub); err != nil || !sub.Reset {
		t.Fatalf("reset %v %v", sub.Reset, err)
	}
	if err := c2.Call(ctx(t), proto.TEventsUnsubscribe, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestPairingAndRevocation(t *testing.T) {
	h := newHarness(t, nil)

	resp := h.localAPI(t, "POST", "/v1/local/pair-codes")
	var nc struct {
		CodeID string `json:"code_id"`
		URL    string `json:"url"`
	}
	json.NewDecoder(resp.Body).Decode(&nc)
	if !strings.HasPrefix(nc.URL, "hedera://pair?") || !strings.Contains(nc.URL, "fp=sha256%3A") {
		t.Fatalf("url %s", nc.URL)
	}
	code := urlParam(t, nc.URL, "c")

	waited := make(chan pairing.CodeStatus, 1)
	go func() {
		resp := h.localAPI(t, "GET", "/v1/local/pair-codes/"+nc.CodeID+"/wait")
		var st pairing.CodeStatus
		json.NewDecoder(resp.Body).Decode(&st)
		waited <- st
	}()

	pr, out := h.pair(t, code)
	if pr.StatusCode != http.StatusOK || !strings.HasPrefix(out["token"], "hdt_") || out["machine_id"] != "m_testtesttesttest" {
		t.Fatalf("pair: %d %v", pr.StatusCode, out)
	}
	if st := <-waited; st.Status != "completed" || st.PairingID != out["pairing_id"] {
		t.Fatalf("wait %+v", st)
	}
	if again, _ := h.pair(t, code); again.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reused code: %d", again.StatusCode)
	}

	// The token works over pinned TLS...
	opts := client.Options{URL: h.remoteURL, Token: out["token"], Fingerprint: h.fp}
	c, err := client.Dial(ctx(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	// ...but not with the wrong pin...
	bad := opts
	bad.Fingerprint = "sha256:AAAA"
	if _, err := client.Dial(ctx(t), bad); err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("wrong pin: %v", err)
	}
	// ...and the local token is not valid remotely.
	if _, err := client.Dial(ctx(t), client.Options{URL: h.remoteURL, Token: h.localTok, Fingerprint: h.fp}); !errors.Is(err, client.ErrUnauthorized) {
		t.Fatalf("local token remotely: %v", err)
	}

	if resp := h.localAPI(t, "DELETE", "/v1/local/pairings/"+out["pairing_id"]); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("revoked connection stayed open")
	}
	if c.CloseStatus() != proto.CloseRevoked {
		t.Fatalf("close status %d", c.CloseStatus())
	}
	if _, err := client.Dial(ctx(t), opts); !errors.Is(err, client.ErrUnauthorized) {
		t.Fatalf("revoked token: %v", err)
	}
}

func urlParam(t *testing.T, raw, key string) string {
	t.Helper()
	_, q, _ := strings.Cut(raw, "?")
	for _, kv := range strings.Split(q, "&") {
		if k, v, _ := strings.Cut(kv, "="); k == key {
			return v
		}
	}
	t.Fatalf("no %s in %s", key, raw)
	return ""
}

func TestPairRateLimit(t *testing.T) {
	h := newHarness(t, nil)
	for i := 0; i < maxFailures; i++ {
		if resp, _ := h.pair(t, "wrong"); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, resp.StatusCode)
		}
	}
	if resp, _ := h.pair(t, "wrong"); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestPeerCheck(t *testing.T) {
	h := newHarness(t, nil)
	h.srv.AllowLoopbackPeers = false
	remote, local := h.srv.RemoteHandler(), h.srv.LocalHandler()
	cases := []struct {
		h      http.Handler
		peer   string
		status int
	}{
		{remote, "192.168.1.20:5000", http.StatusForbidden},
		{remote, "127.0.0.1:5000", http.StatusForbidden},
		{remote, "[2001:db8::1]:5000", http.StatusForbidden},
		{remote, "100.101.102.103:5000", http.StatusUnauthorized},
		{remote, "[fd7a:115c:a1e0::1]:5000", http.StatusUnauthorized},
		{local, "100.101.102.103:5000", http.StatusForbidden},
		{local, "127.0.0.1:5000", http.StatusUnauthorized},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", "/v1/ws", nil)
		req.RemoteAddr = c.peer
		rec := httptest.NewRecorder()
		c.h.ServeHTTP(rec, req)
		if rec.Code != c.status {
			t.Errorf("peer %s: status %d, want %d", c.peer, rec.Code, c.status)
		}
	}
}

func TestFilterTailscale(t *testing.T) {
	mk := func(s string) net.Addr {
		_, n, _ := net.ParseCIDR(s)
		ip, _, _ := net.ParseCIDR(s)
		return &net.IPNet{IP: ip, Mask: n.Mask}
	}
	got := filterTailscale([]net.Addr{
		mk("127.0.0.1/8"), mk("192.168.1.5/24"), mk("fd7a:115c:a1e0::5/128"),
		mk("100.100.1.2/32"), mk("100.63.255.255/32"), mk("100.128.0.1/32"), mk("fe80::1/64"),
	})
	want := []netip.Addr{netip.MustParseAddr("100.100.1.2"), netip.MustParseAddr("fd7a:115c:a1e0::5")}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v", got)
	}
}

func TestTailnetListenerNeverBindsOtherAddresses(t *testing.T) {
	tl := &TailnetListener{
		Port:    0,
		TLS:     &tls.Config{},
		Handler: http.NotFoundHandler(),
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Addrs: func() ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("0.0.0.0")}, nil
		},
		Interval: 10 * time.Millisecond,
	}
	c, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	tl.Run(c)
	if b := tl.Bound(); len(b) != 0 {
		t.Fatalf("bound %v", b)
	}
}

func TestLoopbackListenersRefuseOtherAddresses(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", "[::]:0", "192.168.1.1:0", ":0"} {
		if l, err := ListenLoopback(addr); err == nil {
			l.Close()
			t.Errorf("%s accepted", addr)
		}
	}
}

func postHook(t *testing.T, h *harness, sessionID, token, body string) int {
	t.Helper()
	req, _ := http.NewRequest("POST", h.local.URL+"/v1/hook", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(HookSessionHeader, sessionID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestHookEndpoint(t *testing.T) {
	h := newHarness(t, nil)
	s, err := h.mgr.Create(proto.CreateRequest{Kind: proto.KindClaude, Argv: []string{"/bin/sh", "-c", "read x"}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := h.mgr.Create(proto.CreateRequest{Kind: proto.KindShell, Argv: []string{"/bin/sh", "-c", "read x"}})
	if err != nil {
		t.Fatal(err)
	}
	start := `{"hook_event_name":"SessionStart","session_id":"abc","source":"startup"}`
	cases := []struct {
		name, id, token, body string
		status                int
	}{
		{"no token", s.ID(), "", start, http.StatusUnauthorized},
		{"wrong token", s.ID(), "hht_wrong", start, http.StatusUnauthorized},
		{"another session's token", s.ID(), other.HookToken(), start, http.StatusUnauthorized},
		{"local admin token", s.ID(), h.localTok, start, http.StatusUnauthorized},
		{"unknown session", "s_aaaaaaaaaa", s.HookToken(), start, http.StatusUnauthorized},
		{"bad json", s.ID(), s.HookToken(), "{", http.StatusBadRequest},
		{"no event name", s.ID(), s.HookToken(), `{"session_id":"abc"}`, http.StatusBadRequest},
		{"ok", s.ID(), s.HookToken(), start, http.StatusNoContent},
	}
	for _, c := range cases {
		if got := postHook(t, h, c.id, c.token, c.body); got != c.status {
			t.Errorf("%s: status %d, want %d", c.name, got, c.status)
		}
	}
	if snap := s.Snapshot(); snap.Status != proto.StatusIdle || *snap.Claude.SessionID != "abc" {
		t.Fatalf("hook not applied: %s %+v", snap.Status, snap.Claude)
	}
	if other.Snapshot().Claude != nil {
		t.Fatal("other session affected")
	}
}

func TestHelloAdvertisesClaude(t *testing.T) {
	h := newHarness(t, nil)
	c := h.dialLocal(t)
	if strings.Join(c.Hello.Capabilities, ",") != "shell,claude" {
		t.Fatalf("capabilities %v", c.Hello.Capabilities)
	}
}

func fakeRadixServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var events []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer hrp_good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/link":
			json.NewEncoder(w).Encode(map[string]any{"label": "test phone"})
		case "/v1/events":
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			events = append(events, string(b))
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"sent": true})
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &events
}

func TestPushRoutesFollowPairings(t *testing.T) {
	h := newHarness(t, nil)
	p, err := push.Open(t.TempDir(), push.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	h.srv.Push = p
	radix, _ := fakeRadixServer(t)

	// Pair a "phone" and configure push from its connection.
	resp := h.localAPI(t, "POST", "/v1/local/pair-codes")
	var nc struct{ URL string }
	json.NewDecoder(resp.Body).Decode(&nc)
	_, paired := h.pair(t, urlParam(t, nc.URL, "c"))
	phone, err := client.Dial(ctx(t), client.Options{URL: h.remoteURL, Token: paired["token"], Fingerprint: h.fp})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(phone.Hello.Capabilities, ","), "push") {
		t.Fatalf("capabilities %v", phone.Hello.Capabilities)
	}
	var st proto.PushStatus
	if err := phone.Call(ctx(t), proto.TPushConfigure, proto.PushConfigureRequest{RadixURL: radix.URL, PushToken: "hrp_bad"}, nil); codeOf(err) != proto.CodeInvalidArgument {
		t.Fatalf("bad token: %v", err)
	}
	if err := phone.Call(ctx(t), proto.TPushConfigure, proto.PushConfigureRequest{RadixURL: radix.URL, PushToken: "hrp_good"}, &st); err != nil {
		t.Fatal(err)
	}
	if !st.Configured || st.Label != "test phone" {
		t.Fatalf("status %+v", st)
	}
	// The route belongs to the pairing; the local CLI sees it but has none of its own.
	local := h.dialLocal(t)
	var mine proto.PushStatus
	local.Call(ctx(t), proto.TPushStatus, nil, &mine)
	if mine.Configured {
		t.Fatal("local principal should have no route")
	}
	var routes struct {
		Routes []push.RouteStatus `json:"routes"`
	}
	json.NewDecoder(h.localAPI(t, "GET", "/v1/local/push").Body).Decode(&routes)
	if len(routes.Routes) != 1 || routes.Routes[0].Key != paired["pairing_id"] {
		t.Fatalf("routes %+v", routes)
	}
	if strings.Contains(fmt.Sprint(routes), "hrp_good") {
		t.Fatal("local API exposes the push token")
	}

	// Revoking the pairing removes its push route.
	h.localAPI(t, "DELETE", "/v1/local/pairings/"+paired["pairing_id"])
	json.NewDecoder(h.localAPI(t, "GET", "/v1/local/push").Body).Decode(&routes)
	if len(routes.Routes) != 0 {
		t.Fatalf("route survived revocation: %+v", routes)
	}
}

func TestPushTestEndpoint(t *testing.T) {
	h := newHarness(t, nil)
	p, _ := push.Open(t.TempDir(), push.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	h.srv.Push = p
	radix, events := fakeRadixServer(t)
	local := h.dialLocal(t)
	if err := local.Call(ctx(t), proto.TPushConfigure, proto.PushConfigureRequest{RadixURL: radix.URL, PushToken: "hrp_good"}, nil); err != nil {
		t.Fatal(err)
	}
	var res struct{ Queued int }
	json.NewDecoder(h.localAPI(t, "POST", "/v1/local/push/test").Body).Decode(&res)
	if res.Queued != 1 {
		t.Fatalf("queued %d", res.Queued)
	}
	p.Deliver(ctx(t))
	if len(*events) != 1 || !strings.Contains((*events)[0], `"kind":"test"`) {
		t.Fatalf("events %v", *events)
	}
	if err := local.Call(ctx(t), proto.TPushRemove, nil, nil); err != nil {
		t.Fatal(err)
	}
	var st proto.PushStatus
	local.Call(ctx(t), proto.TPushStatus, nil, &st)
	if st.Configured {
		t.Fatal("route not removed")
	}
}

func TestUnpairRevokesOwnPairing(t *testing.T) {
	h := newHarness(t, nil)
	pairDevice := func() map[string]string {
		resp := h.localAPI(t, "POST", "/v1/local/pair-codes")
		var nc struct{ URL string }
		json.NewDecoder(resp.Body).Decode(&nc)
		_, out := h.pair(t, urlParam(t, nc.URL, "c"))
		return out
	}
	phone, ipad := pairDevice(), pairDevice()
	dial := func(tok string) *client.Client {
		c, err := client.Dial(ctx(t), client.Options{URL: h.remoteURL, Token: tok, Fingerprint: h.fp})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	c1, c2, other := dial(phone["token"]), dial(phone["token"]), dial(ipad["token"])

	if err := c1.Call(ctx(t), proto.TPairingUnpair, nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*client.Client{c1, c2} {
		select {
		case <-c.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("connection stayed open after unpair")
		}
		if c.CloseStatus() != proto.CloseRevoked {
			t.Fatalf("close status %d", c.CloseStatus())
		}
	}
	if _, err := client.Dial(ctx(t), client.Options{URL: h.remoteURL, Token: phone["token"], Fingerprint: h.fp}); !errors.Is(err, client.ErrUnauthorized) {
		t.Fatalf("token still works: %v", err)
	}
	// The other device is untouched.
	var list proto.ListResult
	if err := other.Call(ctx(t), proto.TSessionList, nil, &list); err != nil {
		t.Fatal(err)
	}
	// The local CLI can't unpair (it isn't a pairing).
	if err := h.dialLocal(t).Call(ctx(t), proto.TPairingUnpair, nil, nil); codeOf(err) != proto.CodeInvalidArgument {
		t.Fatalf("local unpair: %v", err)
	}
}
