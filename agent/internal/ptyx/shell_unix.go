//go:build !windows

package ptyx

import (
	"bufio"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

// UserShell returns the user's login shell: $SHELL, else the account
// database, else a platform default.
func UserShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	if u, err := user.Current(); err == nil {
		if s := accountShell(u.Username); s != "" {
			return s
		}
	}
	if runtime.GOOS == "darwin" {
		return "/bin/zsh"
	}
	if _, err := os.Stat("/bin/bash"); err == nil {
		return "/bin/bash"
	}
	return "/bin/sh"
}

func accountShell(username string) string {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("/usr/bin/dscl", ".", "-read", "/Users/"+username, "UserShell").Output()
		if err != nil {
			return ""
		}
		// "UserShell: /bin/zsh"
		if _, v, ok := strings.Cut(strings.TrimSpace(string(out)), ":"); ok {
			return strings.TrimSpace(v)
		}
		return ""
	}
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return ""
	}
	defer f.Close()
	return passwdShell(bufio.NewScanner(f), username)
}

func passwdShell(sc *bufio.Scanner, username string) string {
	for sc.Scan() {
		fields := strings.Split(sc.Text(), ":")
		if len(fields) == 7 && fields[0] == username {
			return fields[6]
		}
	}
	return ""
}

// DefaultShell is the argv for an interactive login shell.
func DefaultShell() []string {
	return []string{UserShell(), "-l"}
}

// WrapLogin runs argv through the user's login shell so PATH and other
// profile settings match a normal login, even under launchd or systemd.
func WrapLogin(argv []string) []string {
	return wrapLogin(UserShell(), argv)
}

func wrapLogin(shell string, argv []string) []string {
	switch filepath.Base(shell) {
	case "sh", "bash", "zsh", "dash", "ksh", "mksh", "ash", "yash":
		// $0 is argv[0]; "$@" the rest. exec replaces the shell.
		return append([]string{shell, "-l", "-c", `exec "$0" "$@"`}, argv...)
	case "fish":
		return append([]string{shell, "-l", "-c", "exec $argv", "--"}, argv...)
	default:
		// csh-family and unknown shells: fall back to a POSIX login shell.
		return append([]string{"/bin/sh", "-l", "-c", `exec "$0" "$@"`}, argv...)
	}
}

// DefaultLocale is used for LANG when the agent's environment has no locale.
func DefaultLocale() string {
	if runtime.GOOS == "darwin" {
		return "en_US.UTF-8"
	}
	return "C.UTF-8"
}
