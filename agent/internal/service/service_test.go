//go:build !windows

package service

import (
	"strings"
	"testing"
)

func TestLaunchdUnit(t *testing.T) {
	u, err := launchdUnit("/Users/t/bin/hedera & co", "/Users/t")
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/Users/t/Library/LaunchAgents/"+LaunchdLabel+".plist" {
		t.Fatal(u.Path)
	}
	for _, want := range []string{
		"<string>/Users/t/bin/hedera &amp; co</string>",
		"<string>run</string>",
		"<key>KeepAlive</key>",
		"<string>/Users/t/Library/Logs/hedera.log</string>",
	} {
		if !strings.Contains(u.Content, want) {
			t.Errorf("plist missing %q", want)
		}
	}
}

func TestSystemdUnit(t *testing.T) {
	u, err := systemdUnit("/home/t/.local/bin/hedera", "/home/t")
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/home/t/.config/systemd/user/hedera.service" {
		t.Fatal(u.Path)
	}
	for _, want := range []string{`ExecStart="/home/t/.local/bin/hedera" run`, "Restart=on-failure", "WantedBy=default.target"} {
		if !strings.Contains(u.Content, want) {
			t.Errorf("unit missing %q", want)
		}
	}
	if _, err := systemdUnit("/tmp/evil\"\nExecStartPre=/bin/rm", "/home/t"); err == nil {
		t.Fatal("unquotable path accepted")
	}
}
