// Package push turns agent events into notifications and delivers them to
// Radix, the push relay (protocol/radix-api.md).
//
// Each paired device that configured push has a route: a Radix URL and a
// push-only token. Notifications are queued on disk and retried, so a Radix
// or network outage delays them instead of losing them.
package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tobiaswadsethdev/hedera/agent/internal/config"
	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
)

// Kinds Radix understands. Prefs are keyed by kind; claude_ended also covers
// claude_ended_abnormally.
const (
	KindShellExited           = "shell_exited"
	KindClaudeTurnFinished    = "claude_turn_finished"
	KindClaudeNeedsInput      = "claude_needs_input"
	KindClaudeFailed          = "claude_failed"
	KindClaudeEnded           = "claude_ended"
	KindClaudeEndedAbnormally = "claude_ended_abnormally"
	KindTest                  = "test"
)

// PrefKinds are the kinds a client can switch on and off.
var PrefKinds = []string{KindShellExited, KindClaudeTurnFinished, KindClaudeNeedsInput, KindClaudeFailed, KindClaudeEnded}

const (
	maxQueue     = 500
	maxAge       = 6 * time.Hour // matches the APNs expiration Radix sets
	maxBackoff   = 5 * time.Minute
	httpTimeout  = 10 * time.Second
	defaultRetry = time.Minute
)

// Notification is one event for Radix.
type Notification struct {
	EventID   string `json:"event_id"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Subtitle  string `json:"subtitle,omitempty"`
	Body      string `json:"body"`
	SessionID string `json:"session_id,omitempty"`
	Turn      *int   `json:"turn,omitempty"`
}

// Route is where one paired device's notifications go.
type Route struct {
	Key           string          `json:"key"` // pairing ID, or "local"
	RadixURL      string          `json:"radix_url"`
	Token         string          `json:"token"`
	PushKey       string          `json:"push_key,omitempty"` // base64url; text is encrypted when set
	Label         string          `json:"label"`
	Prefs         map[string]bool `json:"prefs"`
	CreatedAt     time.Time       `json:"created_at"`
	Broken        string          `json:"broken,omitempty"`
	LastSuccessAt time.Time       `json:"last_success_at,omitzero"`
	LastError     string          `json:"last_error,omitempty"`
}

func (r *Route) wants(kind string) bool {
	if kind == KindTest {
		return true
	}
	if kind == KindClaudeEndedAbnormally {
		kind = KindClaudeEnded
	}
	on, ok := r.Prefs[kind]
	return !ok || on
}

type item struct {
	Route     string       `json:"route"`
	N         Notification `json:"n"`
	Attempts  int          `json:"attempts"`
	NextAt    time.Time    `json:"next_at"`
	CreatedAt time.Time    `json:"created_at"`
}

// Pusher owns the routes and the delivery queue.
type Pusher struct {
	dir       string
	machineID string
	client    *http.Client
	log       *slog.Logger
	now       func() time.Time

	mu     sync.Mutex
	routes map[string]*Route
	queue  []*item
	wake   chan struct{}
}

// Options configures Open. Zero values get defaults.
type Options struct {
	Client *http.Client
	Logger *slog.Logger
	Now    func() time.Time
	// MachineID goes into the additional data of encrypted notifications.
	MachineID string
}

// Open loads routes and the pending queue from dir.
func Open(dir string, o Options) (*Pusher, error) {
	if o.Client == nil {
		o.Client = &http.Client{Timeout: httpTimeout}
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	p := &Pusher{dir: dir, machineID: o.MachineID, client: o.Client, log: o.Logger, now: o.Now, routes: map[string]*Route{}, wake: make(chan struct{}, 1)}
	var routes []*Route
	if err := readJSON(filepath.Join(dir, "push.json"), &routes); err != nil {
		return nil, err
	}
	for _, r := range routes {
		p.routes[r.Key] = r
	}
	if err := readJSON(filepath.Join(dir, "push-queue.json"), &p.queue); err != nil {
		return nil, err
	}
	return p, nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func (p *Pusher) saveRoutesLocked() error {
	routes := make([]*Route, 0, len(p.routes))
	for _, r := range p.routes {
		routes = append(routes, r)
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Key < routes[j].Key })
	b, _ := json.MarshalIndent(routes, "", "  ")
	return config.WriteFileAtomic(filepath.Join(p.dir, "push.json"), append(b, '\n'), 0o600)
}

func (p *Pusher) saveQueueLocked() {
	b, _ := json.Marshal(p.queue)
	if err := config.WriteFileAtomic(filepath.Join(p.dir, "push-queue.json"), b, 0o600); err != nil {
		p.log.Warn("saving push queue", "err", err)
	}
}

func (p *Pusher) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// ValidateRadixURL accepts https URLs, and http only for loopback (tests and
// local development).
func ValidateRadixURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("radix_url must be an https URL")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if ip := net.ParseIP(u.Hostname()); ip == nil || !ip.IsLoopback() {
			return "", errors.New("radix_url must use https")
		}
	default:
		return "", errors.New("radix_url must be an https URL")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// Configure checks the token with Radix and stores the route for key. With a
// pushKey, notification text is encrypted end to end.
func (p *Pusher) Configure(ctx context.Context, key, radixURL, token, pushKey string, prefs map[string]bool) (*Route, error) {
	base, err := ValidateRadixURL(radixURL)
	if err != nil {
		return nil, proto.Errorf(proto.CodeInvalidArgument, "%v", err)
	}
	if !strings.HasPrefix(token, "hrp_") {
		return nil, proto.Errorf(proto.CodeInvalidArgument, "push_token is not a Radix push token")
	}
	if pushKey != "" {
		if _, err := ParseKey(pushKey); err != nil {
			return nil, proto.Errorf(proto.CodeInvalidArgument, "%v", err)
		}
	}
	for k := range prefs {
		if !contains(PrefKinds, k) {
			return nil, proto.Errorf(proto.CodeInvalidArgument, "unknown notification kind %q in prefs", k)
		}
	}
	label, err := p.checkToken(ctx, base, token)
	if err != nil {
		return nil, err
	}
	r := &Route{Key: key, RadixURL: base, Token: token, PushKey: pushKey, Label: label, Prefs: map[string]bool{}, CreatedAt: p.now()}
	for _, k := range PrefKinds {
		on, ok := prefs[k]
		r.Prefs[k] = !ok || on
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.routes[key] = r
	// Anything queued for an older configuration of this key is stale.
	p.dropLocked(func(it *item) bool { return it.Route == key })
	if err := p.saveRoutesLocked(); err != nil {
		return nil, err
	}
	p.log.Info("push configured", "route", key, "radix", base, "label", label, "encrypted", pushKey != "")
	return r, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (p *Pusher) checkToken(ctx context.Context, base, token string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/link", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := p.client.Do(req)
	if err != nil {
		return "", proto.Errorf(proto.CodeUnavailable, "could not reach Radix: %v", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return "", proto.Errorf(proto.CodeInvalidArgument, "Radix rejected the push token")
	case resp.StatusCode != http.StatusOK:
		return "", proto.Errorf(proto.CodeUnavailable, "Radix answered %s", resp.Status)
	}
	var body struct {
		Label string `json:"label"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&body)
	return body.Label, nil
}

// Remove deletes the route for key (and what is queued for it).
func (p *Pusher) Remove(key string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.routes[key]; !ok {
		return false
	}
	delete(p.routes, key)
	p.dropLocked(func(it *item) bool { return it.Route == key })
	if err := p.saveRoutesLocked(); err != nil {
		p.log.Warn("saving push routes", "err", err)
	}
	p.log.Info("push route removed", "route", key)
	return true
}

// Status describes the route for key, without its token.
func (p *Pusher) Status(key string) proto.PushStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.routes[key]
	if !ok {
		return proto.PushStatus{}
	}
	return p.statusLocked(r)
}

func (p *Pusher) statusLocked(r *Route) proto.PushStatus {
	st := proto.PushStatus{Configured: true, RadixURL: r.RadixURL, Label: r.Label, Prefs: r.Prefs, Broken: r.Broken,
		LastError: r.LastError, Encrypted: r.PushKey != ""}
	if !r.LastSuccessAt.IsZero() {
		st.LastSuccessAt = proto.FormatTime(r.LastSuccessAt)
	}
	for _, it := range p.queue {
		if it.Route == r.Key {
			st.Queued++
		}
	}
	return st
}

// RouteStatus is a route's status keyed by its route key.
type RouteStatus struct {
	Key string `json:"key"`
	proto.PushStatus
}

// Routes lists every route, for the local CLI.
func (p *Pusher) Routes() []RouteStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := []RouteStatus{}
	for _, r := range p.routes {
		out = append(out, RouteStatus{Key: r.Key, PushStatus: p.statusLocked(r)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Enqueue queues n for every working route that wants its kind, and returns
// how many routes it was queued for.
func (p *Pusher) Enqueue(n Notification) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	queued := 0
	for _, r := range p.routes {
		if r.Broken != "" || !r.wants(n.Kind) {
			continue
		}
		p.queue = append(p.queue, &item{Route: r.Key, N: n, NextAt: now, CreatedAt: now})
		queued++
	}
	if over := len(p.queue) - maxQueue; over > 0 {
		p.log.Warn("push queue full; dropping oldest", "dropped", over)
		p.queue = append([]*item(nil), p.queue[over:]...)
	}
	if queued > 0 {
		p.saveQueueLocked()
		p.signal()
	}
	return queued
}

func (p *Pusher) dropLocked(match func(*item) bool) {
	kept := p.queue[:0]
	for _, it := range p.queue {
		if !match(it) {
			kept = append(kept, it)
		}
	}
	if len(kept) != len(p.queue) {
		p.queue = kept
		p.saveQueueLocked()
	}
}

// Deliver sends everything that is due, oldest first, and returns when the
// next item will be due (zero if the queue is empty).
func (p *Pusher) Deliver(ctx context.Context) time.Time {
	for ctx.Err() == nil {
		it, r := p.nextDue()
		if it == nil {
			break
		}
		p.attempt(ctx, it, r)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var next time.Time
	for _, it := range p.queue {
		if next.IsZero() || it.NextAt.Before(next) {
			next = it.NextAt
		}
	}
	return next
}

func (p *Pusher) nextDue() (*item, Route) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	p.dropLocked(func(it *item) bool { return now.Sub(it.CreatedAt) > maxAge || p.routes[it.Route] == nil })
	for _, it := range p.queue {
		if !it.NextAt.After(now) {
			return it, *p.routes[it.Route]
		}
	}
	return nil, Route{}
}

// attempt sends one item and applies the response rules from radix-api.md.
func (p *Pusher) attempt(ctx context.Context, it *item, r Route) {
	status, retryAfter, msg := p.post(ctx, r, it.N)
	p.mu.Lock()
	defer p.mu.Unlock()
	route := p.routes[r.Key]
	done := func() { p.dropLocked(func(x *item) bool { return x == it }) }
	switch {
	case status == http.StatusOK:
		done()
		if route != nil {
			route.LastSuccessAt, route.LastError = p.now(), ""
			p.saveRoutesLocked()
		}
		p.log.Info("notification sent", "route", r.Key, "kind", it.N.Kind, "result", msg)
	case status == http.StatusBadRequest:
		done()
		p.noteError(route, "Radix refused a notification: "+msg)
	case status == http.StatusUnauthorized:
		p.dropLocked(func(x *item) bool { return x.Route == r.Key })
		if route != nil {
			route.Broken = "Radix rejected the push token; configure push again from the app"
			p.saveRoutesLocked()
		}
		p.log.Warn("push token rejected; route disabled", "route", r.Key)
	default:
		it.Attempts++
		delay := retryAfter
		if delay == 0 {
			delay = min(time.Duration(1<<min(it.Attempts, 9))*time.Second, maxBackoff)
		}
		it.NextAt = p.now().Add(delay)
		p.saveQueueLocked()
		p.noteError(route, msg)
		p.log.Info("notification delayed", "route", r.Key, "kind", it.N.Kind, "retry_in", delay, "reason", msg)
	}
}

func (p *Pusher) noteError(route *Route, msg string) {
	if route != nil {
		route.LastError = msg
		p.saveRoutesLocked()
	}
}

// post sends one notification. status is 0 on network errors.
func (p *Pusher) post(ctx context.Context, r Route, n Notification) (status int, retryAfter time.Duration, msg string) {
	body, err := p.eventBody(r, n)
	if err != nil {
		return http.StatusBadRequest, 0, err.Error()
	}
	ctx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, r.RadixURL+"/v1/events", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+r.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter = defaultRetry
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			retryAfter = time.Duration(s) * time.Second
		}
	}
	var parsed struct {
		Sent   bool   `json:"sent"`
		Reason string `json:"reason"`
		Error  struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(b, &parsed)
	switch {
	case resp.StatusCode == http.StatusOK && parsed.Sent:
		msg = "sent"
	case resp.StatusCode == http.StatusOK:
		msg = parsed.Reason
	case parsed.Error.Message != "":
		msg = fmt.Sprintf("%s: %s", resp.Status, parsed.Error.Message)
	default:
		msg = resp.Status
	}
	return resp.StatusCode, retryAfter, msg
}

// eventBody is the JSON for POST /v1/events: plaintext, or for a route with
// a key, the text encrypted with a fresh nonce.
func (p *Pusher) eventBody(r Route, n Notification) ([]byte, error) {
	if r.PushKey == "" {
		return json.Marshal(n)
	}
	key, err := ParseKey(r.PushKey)
	if err != nil {
		return nil, err
	}
	enc, err := Encrypt(key, p.machineID, n)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		EventID   string `json:"event_id"`
		Kind      string `json:"kind"`
		Encrypted string `json:"encrypted"`
		SessionID string `json:"session_id,omitempty"`
		Turn      *int   `json:"turn,omitempty"`
	}{n.EventID, n.Kind, enc, n.SessionID, n.Turn})
}

// Run delivers queued notifications until ctx is done.
func (p *Pusher) Run(ctx context.Context) {
	for {
		next := p.Deliver(ctx)
		var timer *time.Timer
		var fire <-chan time.Time
		if !next.IsZero() {
			timer = time.NewTimer(max(next.Sub(p.now()), 10*time.Millisecond))
			fire = timer.C
		}
		select {
		case <-ctx.Done():
		case <-p.wake:
		case <-fire:
		}
		if timer != nil {
			timer.Stop()
		}
		if ctx.Err() != nil {
			return
		}
	}
}
