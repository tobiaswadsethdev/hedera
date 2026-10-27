package server

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"sync"
	"time"
)

var tailscaleRanges = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
}

// IsTailscale reports whether a is in Tailscale's address ranges.
func IsTailscale(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range tailscaleRanges {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// TailscaleAddrs returns this machine's Tailscale addresses, IPv4 first.
func TailscaleAddrs() ([]netip.Addr, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	return filterTailscale(addrs), nil
}

func filterTailscale(addrs []net.Addr) []netip.Addr {
	var out []netip.Addr
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if na, ok := netip.AddrFromSlice(ip); ok && IsTailscale(na) {
			out = append(out, na.Unmap())
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Is4() != out[j].Is4() {
			return out[i].Is4()
		}
		return out[i].Less(out[j])
	})
	return out
}

// TailnetListener serves an HTTP handler over TLS on every Tailscale address
// of this machine, and nowhere else. It rechecks addresses periodically, so it
// picks up Tailscale coming up after the agent and drops addresses that go
// away.
type TailnetListener struct {
	Port     int
	TLS      *tls.Config
	Handler  http.Handler
	Log      *slog.Logger
	Interval time.Duration // default 10s
	// Addrs lists the addresses to bind; default TailscaleAddrs.
	Addrs func() ([]netip.Addr, error)

	srv *http.Server

	mu        sync.Mutex
	listeners map[netip.Addr]net.Listener
	warned    bool
}

// Run serves until ctx is done.
func (t *TailnetListener) Run(ctx context.Context) {
	if t.Interval == 0 {
		t.Interval = 10 * time.Second
	}
	if t.Addrs == nil {
		t.Addrs = TailscaleAddrs
	}
	t.srv = newHTTPServer(t.Handler, t.Log)
	t.listeners = map[netip.Addr]net.Listener{}
	tick := time.NewTicker(t.Interval)
	defer tick.Stop()
	for {
		t.refresh()
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			t.srv.Shutdown(shutdownCtx)
			cancel()
			return
		case <-tick.C:
		}
	}
}

// Bound returns the addresses currently being served.
func (t *TailnetListener) Bound() []netip.Addr {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]netip.Addr, 0, len(t.listeners))
	for a := range t.listeners {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Less(out[j]) })
	return out
}

func (t *TailnetListener) refresh() {
	addrs, err := t.Addrs()
	if err != nil {
		t.Log.Warn("listing interface addresses", "err", err)
		return
	}
	want := map[netip.Addr]bool{}
	for _, a := range addrs {
		if IsTailscale(a) { // defence in depth: never bind anything else
			want[a] = true
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for a, l := range t.listeners {
		if !want[a] {
			l.Close()
			delete(t.listeners, a)
			t.Log.Info("stopped listening", "addr", a)
		}
	}
	for a := range want {
		if _, ok := t.listeners[a]; ok {
			continue
		}
		hostport := net.JoinHostPort(a.String(), strconv.Itoa(t.Port))
		l, err := net.Listen("tcp", hostport)
		if err != nil {
			t.Log.Warn("cannot listen", "addr", hostport, "err", err)
			continue
		}
		t.listeners[a] = l
		t.Log.Info("listening", "addr", hostport)
		go func() {
			err := t.srv.Serve(tls.NewListener(l, t.TLS))
			if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				t.Log.Warn("serve ended", "addr", hostport, "err", err)
			}
		}()
	}
	if len(t.listeners) == 0 && !t.warned {
		t.Log.Warn("no Tailscale address found; waiting for Tailscale to come up")
	}
	t.warned = len(t.listeners) == 0
}

// ListenLoopback listens on a loopback address and refuses anything else.
// The local API uses it, and so does the loopback TLS listener that lets the
// remote code path (pairing, TLS pinning) run on one machine without
// Tailscale.
func ListenLoopback(addr string) (net.Listener, error) {
	if err := requireLoopback(addr); err != nil {
		return nil, err
	}
	return net.Listen("tcp", addr)
}

// Serve serves h on l until ctx is done.
func Serve(ctx context.Context, l net.Listener, h http.Handler, log *slog.Logger) error {
	return serveUntil(ctx, l, h, log)
}

func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return errors.New("refusing to listen on non-loopback address " + addr)
	}
	return nil
}

func serveUntil(ctx context.Context, l net.Listener, h http.Handler, log *slog.Logger) error {
	srv := newHTTPServer(h, log)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()
	log.Info("listening", "addr", l.Addr().String())
	if err := srv.Serve(l); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func newHTTPServer(h http.Handler, log *slog.Logger) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelDebug),
	}
}
