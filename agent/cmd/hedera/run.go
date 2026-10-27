package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tobiaswadsethdev/hedera/agent/internal/config"
	"github.com/tobiaswadsethdev/hedera/agent/internal/ids"
	"github.com/tobiaswadsethdev/hedera/agent/internal/logging"
	"github.com/tobiaswadsethdev/hedera/agent/internal/pairing"
	"github.com/tobiaswadsethdev/hedera/agent/internal/push"
	"github.com/tobiaswadsethdev/hedera/agent/internal/server"
	"github.com/tobiaswadsethdev/hedera/agent/internal/session"
	"github.com/tobiaswadsethdev/hedera/agent/internal/tlsutil"
)

func cmdRun(args []string) error {
	fs := newFlags("run", usageFor("run"))
	devLoopback := fs.Bool("dev-loopback", false, "also serve the remote (TLS) API on 127.0.0.1, for testing pairing without Tailscale")
	verbose := fs.Bool("v", false, "debug logging")
	logFormat := fs.String("log-format", "text", "log line format: text or json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	format, err := logging.ParseFormat(*logFormat)
	if err != nil {
		return err
	}
	log := logging.New(os.Stderr, format, level)

	dir, err := config.Dir()
	if err != nil {
		return err
	}
	cfg, err := config.Load(dir)
	if err != nil {
		return err
	}
	localToken, err := config.LocalToken(dir)
	if err != nil {
		return err
	}
	hostname := server.Hostname()
	cert, err := tlsutil.LoadOrCreate(filepath.Join(dir, "tls"), "hedera "+hostname)
	if err != nil {
		return err
	}
	fp := tlsutil.Fingerprint(cert.Leaf)
	store, err := pairing.Open(filepath.Join(dir, "pairings.json"), nil)
	if err != nil {
		return err
	}

	// The local listener doubles as a single-instance lock, so bind it first.
	localAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.LocalPort))
	localLn, err := server.ListenLoopback(localAddr)
	if err != nil {
		return fmt.Errorf("cannot listen on %s (is another hedera agent running?): %w", localAddr, err)
	}

	mgr := session.NewManager(session.Config{
		MachineID:       cfg.MachineID,
		ScrollbackBytes: cfg.ScrollbackBytes,
		MaxSessions:     cfg.MaxSessions,
		HookURL:         "http://" + localAddr + "/v1/hook",
		Logger:          log,
	})
	pusher, err := push.Open(dir, push.Options{Logger: log})
	if err != nil {
		return fmt.Errorf("loading push state: %w", err)
	}
	tlsCfg := tlsutil.ServerConfig(cert)
	srv := &server.Server{
		Manager:            mgr,
		Pairings:           store,
		LocalToken:         localToken,
		MachineID:          cfg.MachineID,
		BootID:             ids.BootID(),
		Hostname:           hostname,
		Version:            version,
		Log:                log,
		AllowLoopbackPeers: *devLoopback,
		Push:               pusher,
	}
	tailnet := &server.TailnetListener{Port: cfg.Port, TLS: tlsCfg, Handler: srv.RemoteHandler(), Log: log}
	dns := &dnsCache{configured: cfg.DNSName}
	srv.PairInfo = func() server.PairInfo {
		// IPv4 alone keeps the QR code small; IPv6 only if there is no IPv4.
		var addrs []netip.Addr
		for _, a := range tailnet.Bound() {
			if a.Is4() {
				addrs = append(addrs, a)
			}
		}
		if len(addrs) == 0 {
			addrs = tailnet.Bound()
		}
		if *devLoopback {
			addrs = append(addrs, netip.MustParseAddr("127.0.0.1"))
		}
		return server.PairInfo{Addrs: addrs, DNSName: dns.get(tailnet.Bound()), Port: cfg.Port, Fingerprint: fp}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("hedera agent starting", "version", version, "machine", cfg.MachineID, "state", dir, "fingerprint", fp)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(5)
	go func() { defer wg.Done(); mgr.Run(ctx) }()
	go func() { defer wg.Done(); pusher.Run(ctx) }()
	go func() {
		defer wg.Done()
		pusher.Watch(ctx, push.Source{
			Bus:      mgr.Bus(),
			BootID:   srv.BootID,
			Hostname: hostname,
			LastInput: func(id string) time.Time {
				if s, err := mgr.Get(id); err == nil {
					return s.LastInputAt()
				}
				return time.Time{}
			},
		})
	}()
	go func() { defer wg.Done(); tailnet.Run(ctx) }()
	go func() { defer wg.Done(); errs <- server.Serve(ctx, localLn, srv.LocalHandler(), log) }()
	if *devLoopback {
		devAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port))
		ln, err := server.ListenLoopback(devAddr)
		if err != nil {
			stop()
			wg.Wait()
			return err
		}
		log.Warn("serving the remote API on loopback for testing", "addr", devAddr)
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- server.Serve(ctx, tls.NewListener(ln, tlsCfg), srv.RemoteHandler(), log)
		}()
	}

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errs:
		stop()
	}
	log.Info("shutting down")
	srv.CloseConnections("", 1001, "agent shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	mgr.Shutdown(shutdownCtx)
	cancel()
	wg.Wait()
	return runErr
}

// dnsCache resolves the machine's MagicDNS name from its Tailscale address,
// for pairing URLs. Only *.ts.net answers are trusted, since other reverse
// DNS names may not resolve from the phone.
type dnsCache struct {
	configured string

	mu     sync.Mutex
	name   string
	expiry time.Time
}

func (d *dnsCache) get(addrs []netip.Addr) string {
	if d.configured != "" {
		return d.configured
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if time.Now().Before(d.expiry) {
		return d.name
	}
	d.name, d.expiry = "", time.Now().Add(5*time.Minute)
	for _, a := range addrs {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		names, err := net.DefaultResolver.LookupAddr(ctx, a.String())
		cancel()
		if err != nil {
			continue
		}
		for _, n := range names {
			n = strings.TrimSuffix(n, ".")
			if strings.HasSuffix(n, ".ts.net") {
				d.name = n
				return n
			}
		}
	}
	return ""
}

var errNotRunning = errors.New("the hedera agent is not running on this machine (start it with 'hedera run' or 'hedera install-service')")
