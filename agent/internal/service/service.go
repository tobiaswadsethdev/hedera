// Package service installs `hedera run` as a per-user background service:
// a launchd agent on macOS and a systemd user unit on Linux. Windows (a logon
// scheduled task) arrives in Phase 5.
package service

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
)

const (
	LaunchdLabel = "io.github.tobiaswadsethdev.hedera"
	SystemdUnit  = "hedera.service"
)

// Unit describes the generated service file.
type Unit struct {
	Path    string // where it is installed
	Content string
}

// Plan returns the service file for this platform, running exe.
func Plan(exe, home string) (Unit, error) {
	switch runtime.GOOS {
	case "darwin":
		return launchdUnit(exe, home)
	case "linux":
		return systemdUnit(exe, home)
	default:
		return Unit{}, fmt.Errorf("install-service is not supported on %s yet", runtime.GOOS)
	}
}

var launchdTmpl = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{.Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{.Exe}}</string>
		<string>run</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ProcessType</key>
	<string>Interactive</string>
	<key>StandardOutPath</key>
	<string>{{.Log}}</string>
	<key>StandardErrorPath</key>
	<string>{{.Log}}</string>
</dict>
</plist>
`))

func launchdUnit(exe, home string) (Unit, error) {
	var b bytes.Buffer
	err := launchdTmpl.Execute(&b, map[string]string{
		"Label": LaunchdLabel,
		"Exe":   xmlEscape(exe),
		"Log":   xmlEscape(filepath.Join(home, "Library", "Logs", "hedera.log")),
	})
	return Unit{Path: filepath.Join(home, "Library", "LaunchAgents", LaunchdLabel+".plist"), Content: b.String()}, err
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func systemdUnit(exe, home string) (Unit, error) {
	if strings.ContainsAny(exe, "\n\"\\") {
		return Unit{}, errors.New("executable path contains characters systemd can't quote")
	}
	content := fmt.Sprintf(`[Unit]
Description=Hedera agent (terminals and Claude Code sessions for the Hedera app)
After=network-online.target
Wants=network-online.target

[Service]
ExecStart="%s" run
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
`, strings.ReplaceAll(exe, "%", "%%"))
	return Unit{Path: filepath.Join(home, ".config", "systemd", "user", SystemdUnit), Content: content}, nil
}

// Install writes the unit and loads it. It returns notes to show the user.
func Install(u Unit) ([]string, error) {
	if err := os.MkdirAll(filepath.Dir(u.Path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(u.Path, []byte(u.Content), 0o644); err != nil {
		return nil, err
	}
	switch runtime.GOOS {
	case "darwin":
		target := fmt.Sprintf("gui/%d", os.Getuid())
		// Unload a previous version first; failure just means none was loaded.
		exec.Command("launchctl", "bootout", target+"/"+LaunchdLabel).Run()
		if out, err := exec.Command("launchctl", "bootstrap", target, u.Path).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("launchctl bootstrap: %v: %s", err, out)
		}
		return []string{"Logs: ~/Library/Logs/hedera.log"}, nil
	case "linux":
		if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
			return nil, fmt.Errorf("systemctl daemon-reload: %v: %s", err, out)
		}
		if out, err := exec.Command("systemctl", "--user", "enable", "--now", SystemdUnit).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("systemctl enable: %v: %s", err, out)
		}
		exec.Command("systemctl", "--user", "restart", SystemdUnit).Run()
		notes := []string{"Logs: journalctl --user -u hedera -f"}
		if out, _ := exec.Command("loginctl", "show-user", os.Getenv("USER"), "-p", "Linger").Output(); !strings.Contains(string(out), "Linger=yes") {
			notes = append(notes, "To keep hedera running while you are logged out: loginctl enable-linger $USER")
		}
		return notes, nil
	}
	return nil, nil
}

// Uninstall stops the service and removes the unit.
func Uninstall(u Unit) error {
	switch runtime.GOOS {
	case "darwin":
		exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), LaunchdLabel)).Run()
	case "linux":
		exec.Command("systemctl", "--user", "disable", "--now", SystemdUnit).Run()
	}
	if err := os.Remove(u.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if runtime.GOOS == "linux" {
		exec.Command("systemctl", "--user", "daemon-reload").Run()
	}
	return nil
}
