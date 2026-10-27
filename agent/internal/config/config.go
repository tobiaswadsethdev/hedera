// Package config locates the agent's state directory and loads its settings.
//
// Layout of the state directory (mode 0700):
//
//	config.json     settings and the machine ID
//	local.token     local admin token for the 127.0.0.1 API (0600)
//	pairings.json   paired clients; token hashes only (0600)
//	remotes.json    agents this CLI has paired with as a client (0600)
//	tls/cert.pem    self-signed certificate
//	tls/key.pem     its private key (0600)
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tobiaswadsethdev/hedera/agent/internal/ids"
)

const (
	DefaultPort      = 7373
	DefaultLocalPort = 7374
)

// Config is the content of config.json. Zero values mean "use the default".
type Config struct {
	MachineID       string `json:"machine_id"`
	Port            int    `json:"port,omitempty"`
	LocalPort       int    `json:"local_port,omitempty"`
	ScrollbackBytes int    `json:"scrollback_bytes,omitempty"`
	MaxSessions     int    `json:"max_sessions,omitempty"`
	// DNSName is advertised in pairing QR codes, e.g. "studio.tail1234.ts.net".
	DNSName string `json:"dns_name,omitempty"`
	// AllowedUsers and AllowedTags, when either is set, limit remote
	// connections to tailnet nodes owned by these logins (e.g.
	// "you@github") or carrying these tags (e.g. "tag:servers"), as
	// `tailscale whois` reports them.
	AllowedUsers []string `json:"allowed_users,omitempty"`
	AllowedTags  []string `json:"allowed_tags,omitempty"`
}

// Dir returns the state directory: $HEDERA_HOME, else <user config dir>/hedera.
func Dir() (string, error) {
	if d := os.Getenv("HEDERA_HOME"); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "hedera"), nil
}

// EnsureDir creates dir with mode 0700, or tightens an existing one.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

// Load reads config.json from dir, creating it (with a new machine ID) if
// needed.
func Load(dir string) (*Config, error) {
	if err := EnsureDir(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "config.json")
	var c Config
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, &c); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	if c.MachineID == "" {
		c.MachineID = ids.MachineID()
		out, _ := json.MarshalIndent(c, "", "  ")
		if err := WriteFileAtomic(path, append(out, '\n'), 0o600); err != nil {
			return nil, err
		}
	}
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	if c.LocalPort == 0 {
		c.LocalPort = DefaultLocalPort
	}
	return &c, nil
}

// LocalToken returns the local admin token, creating it on first use.
func LocalToken(dir string) (string, error) {
	path := filepath.Join(dir, "local.token")
	b, err := os.ReadFile(path)
	if err == nil {
		return strings.TrimSpace(string(b)), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := EnsureDir(dir); err != nil {
		return "", err
	}
	tok := ids.Secret("hlt_")
	if err := WriteFileAtomic(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

// WriteFileAtomic writes data to a temporary file next to path and renames it
// into place, so readers never see a partial file.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
