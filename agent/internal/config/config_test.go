package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadCreatesStableMachineID(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hedera")
	a, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a.MachineID, "m_") || a.Port != DefaultPort || a.LocalPort != DefaultLocalPort {
		t.Fatalf("config %+v", a)
	}
	b, err := Load(dir)
	if err != nil || b.MachineID != a.MachineID {
		t.Fatalf("machine ID changed: %v %v", b, err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode %v", fi.Mode())
		}
	}
}

func TestLocalTokenIsStableAndPrivate(t *testing.T) {
	dir := t.TempDir()
	a, err := LocalToken(dir)
	if err != nil || !strings.HasPrefix(a, "hlt_") {
		t.Fatalf("%q %v", a, err)
	}
	if b, _ := LocalToken(dir); b != a {
		t.Fatal("token changed")
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(filepath.Join(dir, "local.token")); fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", fi.Mode())
		}
	}
}
