package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/tobiaswadsethdev/hedera/agent/internal/client"
	"github.com/tobiaswadsethdev/hedera/agent/internal/config"
)

// Remote is an agent this CLI paired with, stored in remotes.json.
type Remote struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Token       string `json:"token"`
	Fingerprint string `json:"fingerprint"`
	MachineID   string `json:"machine_id"`
	PairingID   string `json:"pairing_id"`
}

func remotesPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "remotes.json"), nil
}

func loadRemotes() ([]Remote, error) {
	path, err := remotesPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rs []Remote
	return rs, json.Unmarshal(b, &rs)
}

func saveRemotes(rs []Remote) error {
	path, err := remotesPath()
	if err != nil {
		return err
	}
	if err := config.EnsureDir(filepath.Dir(path)); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(rs, "", "  ")
	return config.WriteFileAtomic(path, append(b, '\n'), 0o600)
}

// targetFlag adds --remote to a client command.
func targetFlag(fs *flag.FlagSet) *string {
	return fs.String("remote", "", "talk to a paired remote agent (see 'hedera remote ls') instead of this machine's")
}

// clientOptions says how to reach the agent: a named remote, or this
// machine's agent over the local API.
func clientOptions(remote string) (client.Options, error) {
	if remote != "" {
		rs, err := loadRemotes()
		if err != nil {
			return client.Options{}, err
		}
		for _, r := range rs {
			if r.Name == remote {
				return client.Options{URL: r.URL, Token: r.Token, Fingerprint: r.Fingerprint, Version: version}, nil
			}
		}
		return client.Options{}, fmt.Errorf("no remote named %q (see 'hedera remote ls')", remote)
	}
	dir, err := config.Dir()
	if err != nil {
		return client.Options{}, err
	}
	cfg, err := config.Load(dir)
	if err != nil {
		return client.Options{}, err
	}
	tok, err := config.LocalToken(dir)
	if err != nil {
		return client.Options{}, err
	}
	return client.Options{
		URL:     "ws://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.LocalPort)) + "/v1/ws",
		Token:   tok,
		Version: version,
	}, nil
}

func dial(remote string) (*client.Client, error) {
	o, err := clientOptions(remote)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := client.Dial(ctx, o)
	if remote == "" && errors.Is(err, syscall.ECONNREFUSED) {
		return nil, errNotRunning
	}
	return c, err
}

func callCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 15*time.Second)
}

// localAPI calls the agent's local-only HTTP API.
func localAPI(ctx context.Context, method, path string, out any) error {
	dir, err := config.Dir()
	if err != nil {
		return err
	}
	cfg, err := config.Load(dir)
	if err != nil {
		return err
	}
	tok, err := config.LocalToken(dir)
	if err != nil {
		return err
	}
	url := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.LocalPort)) + path
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if errors.Is(err, syscall.ECONNREFUSED) {
		return errNotRunning
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, body)
	}
	if out != nil && len(body) > 0 {
		return json.Unmarshal(body, out)
	}
	return nil
}
