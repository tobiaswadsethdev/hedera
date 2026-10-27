//go:build windows

package ptyx

import (
	"os"
	"os/exec"
)

// UserShell prefers PowerShell 7, then Windows PowerShell, then %COMSPEC%.
func UserShell() string {
	for _, s := range []string{"pwsh.exe", "powershell.exe"} {
		if p, err := exec.LookPath(s); err == nil {
			return p
		}
	}
	if c := os.Getenv("COMSPEC"); c != "" {
		return c
	}
	return "cmd.exe"
}

func DefaultShell() []string { return []string{UserShell()} }

// WrapLogin is the identity on Windows: there is no login-shell PATH split.
func WrapLogin(argv []string) []string { return argv }

func DefaultLocale() string { return "" }
