// Package update fetches release builds of hedera and swaps them in for the
// running binary.
//
// A release is a directory of files served over HTTPS:
//
//	manifest.json        {"version": "v0.3.0", "files": {"hedera_darwin_arm64": "<sha256 hex>", …}}
//	hedera_<os>_<arch>   the binaries
//
// The default location is the latest GitHub release of this (public) repository,
// whose "releases/latest/download/<file>" URLs always point at the newest
// release. Binaries are checked against the manifest's SHA-256, which guards
// against corrupt or mixed-up downloads but not against whoever controls the
// release location: releases are not signed yet.
package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// DefaultURL is where releases are published (see scripts/release.sh).
const DefaultURL = "https://github.com/tobiaswadsethdev/hedera/releases/latest/download"

// maxBinary caps a download, so a wrong URL can't fill the disk.
const maxBinary = 256 << 20

// Manifest describes one release.
type Manifest struct {
	Version string            `json:"version"`
	Files   map[string]string `json:"files"` // file name → SHA-256, hex
}

// AssetName is the binary for this platform, e.g. "hedera_darwin_arm64".
func AssetName() string {
	return "hedera_" + runtime.GOOS + "_" + runtime.GOARCH
}

// Fetch downloads and parses the manifest at base.
func Fetch(ctx context.Context, c *http.Client, base string) (*Manifest, error) {
	b, err := get(ctx, c, strings.TrimSuffix(base, "/")+"/manifest.json", 1<<20)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("manifest.json: %w", err)
	}
	if _, ok := parseVersion(m.Version); !ok {
		return nil, fmt.Errorf("manifest.json: bad version %q", m.Version)
	}
	return &m, nil
}

// Download fetches this platform's binary from base and checks it against m.
func Download(ctx context.Context, c *http.Client, base string, m *Manifest) ([]byte, error) {
	name := AssetName()
	want, ok := m.Files[name]
	if !ok {
		return nil, fmt.Errorf("release %s has no build for %s/%s", m.Version, runtime.GOOS, runtime.GOARCH)
	}
	b, err := get(ctx, c, strings.TrimSuffix(base, "/")+"/"+name, maxBinary)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, want) {
		// Most likely a newer release was published between the two requests.
		return nil, fmt.Errorf("%s: checksum mismatch (got %s, manifest says %s)", name, got, want)
	}
	return b, nil
}

func get(ctx context.Context, c *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("GET %s: response larger than %d bytes", url, limit)
	}
	return b, nil
}

// Install replaces the file at exe with data. It writes a temporary file next
// to exe and renames it over, so the running process keeps its old inode
// and nothing ever sees a half-written binary.
func Install(exe string, data []byte) error {
	if !bytes.HasPrefix(data, []byte("\x7fELF")) && !isMachO(data) {
		return errors.New("downloaded file is not an executable for this platform")
	}
	f, err := os.CreateTemp(filepath.Dir(exe), "."+filepath.Base(exe)+".update.*")
	if err != nil {
		return fmt.Errorf("cannot write next to %s: %w", exe, err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o755); err != nil {
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
	return os.Rename(tmp, exe)
}

func isMachO(b []byte) bool {
	return bytes.HasPrefix(b, []byte{0xcf, 0xfa, 0xed, 0xfe}) || // 64-bit, little endian
		bytes.HasPrefix(b, []byte{0xca, 0xfe, 0xba, 0xbe}) // universal
}

// Newer reports whether version a is newer than b. Versions look like
// "v1.2.3" or "v1.2.3-rc.1"; anything else (a "dev" build, say) counts as
// older than every release.
func Newer(a, b string) bool {
	va, okA := parseVersion(a)
	vb, okB := parseVersion(b)
	switch {
	case !okA:
		return false
	case !okB:
		return true
	}
	for i := range 3 {
		if va.n[i] != vb.n[i] {
			return va.n[i] > vb.n[i]
		}
	}
	// A release beats its own pre-releases; pre-releases compare as strings.
	switch {
	case va.pre == vb.pre:
		return false
	case va.pre == "":
		return true
	case vb.pre == "":
		return false
	}
	return va.pre > vb.pre
}

type version struct {
	n   [3]int
	pre string
}

func parseVersion(s string) (version, bool) {
	var v version
	s, ok := strings.CutPrefix(s, "v")
	if !ok {
		return v, false
	}
	s, v.pre, _ = strings.Cut(s, "-")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v.n[i] = n
	}
	return v, true
}
