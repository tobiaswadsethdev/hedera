package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/tobiaswadsethdev/hedera/agent/internal/config"
	"github.com/tobiaswadsethdev/hedera/agent/internal/session"
	"github.com/tobiaswadsethdev/hedera/agent/internal/update"
)

func cmdUpgrade(args []string) error {
	fs := newFlags("upgrade", usageFor("upgrade"))
	check := fs.Bool("check", false, "only report whether a newer release exists")
	url := fs.String("url", "", "fetch releases from here instead of update_url / the default")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return errors.New("upgrade is not supported on Windows yet")
	}
	dir, err := config.Dir()
	if err != nil {
		return err
	}
	cfg, err := config.Load(dir)
	if err != nil {
		return err
	}
	base := updateURL(cfg, *url)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	m, err := update.Fetch(ctx, http.DefaultClient, base)
	if err != nil {
		return err
	}
	if !update.Newer(m.Version, version) {
		fmt.Printf("hedera %s is up to date (latest release: %s)\n", version, m.Version)
		return nil
	}
	if *check {
		fmt.Printf("hedera %s is available; this is %s\n", m.Version, version)
		return nil
	}
	exe, err := stableExecutable()
	if err != nil {
		return err
	}
	data, err := update.Download(ctx, http.DefaultClient, base, m)
	if err != nil {
		return err
	}
	if err := update.Install(exe, data); err != nil {
		return err
	}
	fmt.Printf("installed hedera %s at %s\n", m.Version, exe)
	switch n := runningSessions(); {
	case !cfg.AutoUpdates():
		fmt.Println("auto_update is off in config.json, so restart the agent to use it")
	case n > 0:
		fmt.Printf("the agent restarts onto it once its %d running session(s) have ended\n", n)
	default:
		fmt.Println("a running agent restarts onto it within a minute")
	}
	return nil
}

func updateURL(cfg *config.Config, flag string) string {
	switch {
	case flag != "":
		return flag
	case cfg.UpdateURL != "":
		return cfg.UpdateURL
	}
	return update.DefaultURL
}

// selfUpdater runs inside the agent. It installs new releases over the
// agent's binary (unless this is a dev build), and restarts the agent onto a
// replaced binary, whoever replaced it, once no sessions are running, since
// a restart ends every session.
type selfUpdater struct {
	exe     string
	base    string
	fetch   bool // check for releases; off for dev builds
	mgr     *session.Manager
	log     *slog.Logger
	restart func()

	installed string // version put on disk, waiting for a restart
	waiting   bool   // logged that sessions are holding up a restart
}

const (
	updatePoll  = 30 * time.Second
	updateFirst = time.Minute
	updateEvery = 6 * time.Hour
)

func (u *selfUpdater) run(ctx context.Context) {
	current, err := os.Stat(u.exe)
	if err != nil {
		u.log.Warn("auto-update off: cannot stat own binary", "err", err)
		return
	}
	var changed os.FileInfo
	nextFetch := time.Now().Add(updateFirst)
	t := time.NewTicker(updatePoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if u.fetch && time.Now().After(nextFetch) {
			nextFetch = time.Now().Add(updateEvery)
			if err := u.fetchRelease(ctx); err != nil {
				u.log.Warn("checking for updates", "err", err)
			}
		}
		fi, err := os.Stat(u.exe)
		if err != nil || sameFile(fi, current) {
			changed = nil
			continue
		}
		// Wait a poll for the new file to settle, in case it is still being
		// written (a `go build -o` straight over it, say).
		if changed == nil || !sameFile(fi, changed) {
			changed = fi
			continue
		}
		if err := runs(u.exe); err != nil {
			u.log.Warn("not restarting onto the replaced binary, it does not run", "exe", u.exe, "err", err)
			current, changed = fi, nil
			continue
		}
		if !u.mgr.CloseIfIdle() {
			if !u.waiting {
				u.log.Info("binary replaced; restarting once no sessions are running", "exe", u.exe)
				u.waiting = true
			}
			continue
		}
		u.log.Info("binary replaced; restarting onto it", "exe", u.exe)
		u.restart()
		return
	}
}

func (u *selfUpdater) fetchRelease(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	m, err := update.Fetch(ctx, http.DefaultClient, u.base)
	if err != nil {
		return err
	}
	if !update.Newer(m.Version, version) || m.Version == u.installed {
		return nil
	}
	u.log.Info("downloading update", "version", m.Version)
	data, err := update.Download(ctx, http.DefaultClient, u.base, m)
	if err != nil {
		return err
	}
	if err := update.Install(u.exe, data); err != nil {
		return err
	}
	u.installed = m.Version
	u.log.Info("update installed", "version", m.Version, "exe", u.exe)
	return nil
}

func sameFile(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// runs checks that exe starts and answers `hedera version`, so a broken or
// wrong-architecture binary can't put the agent in a crash loop.
func runs(exe string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, "version").Output()
	if err != nil {
		return err
	}
	if !strings.HasPrefix(string(out), "hedera ") {
		return fmt.Errorf("unexpected output from 'version': %q", out)
	}
	return nil
}
