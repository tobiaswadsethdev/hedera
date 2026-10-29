package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v0.2.0", "v0.1.0", true},
		{"v0.1.0", "v0.2.0", false},
		{"v0.1.0", "v0.1.0", false},
		{"v0.10.0", "v0.9.9", true},
		{"v1.0.0", "v1.0.0-rc.1", true},
		{"v1.0.0-rc.1", "v1.0.0", false},
		{"v1.0.0-rc.2", "v1.0.0-rc.1", true},
		{"v0.1.0", "dev", true},
		{"dev", "v0.1.0", false},
		{"1.0.0", "v0.1.0", false},
		{"v1.0", "v0.1.0", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestFetchDownloadInstall(t *testing.T) {
	bin := append([]byte("\x7fELF"), make([]byte, 100)...)
	if strings.HasPrefix(AssetName(), "hedera_darwin") {
		bin = append([]byte{0xcf, 0xfa, 0xed, 0xfe}, make([]byte, 100)...)
	}
	sum := sha256.Sum256(bin)
	files := map[string]string{AssetName(): hex.EncodeToString(sum[:])}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rel/manifest.json":
			json.NewEncoder(w).Encode(Manifest{Version: "v1.2.3", Files: files})
		case "/rel/" + AssetName():
			w.Write(bin)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	m, err := Fetch(ctx, srv.Client(), srv.URL+"/rel/")
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != "v1.2.3" {
		t.Fatalf("version %q", m.Version)
	}
	got, err := Download(ctx, srv.Client(), srv.URL+"/rel", m)
	if err != nil {
		t.Fatal(err)
	}

	exe := filepath.Join(t.TempDir(), "hedera")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Install(exe, got); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != string(bin) {
		t.Fatal("binary not replaced")
	}
	if fi, _ := os.Stat(exe); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if entries, _ := os.ReadDir(filepath.Dir(exe)); len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}

	m.Files[AssetName()] = strings.Repeat("0", 64)
	if _, err := Download(ctx, srv.Client(), srv.URL+"/rel", m); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("want checksum error, got %v", err)
	}
	if err := Install(exe, []byte("#!/bin/sh\n")); err == nil {
		t.Fatal("installed a non-binary")
	}
}
