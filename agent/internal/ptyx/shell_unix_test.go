//go:build !windows

package ptyx

import (
	"bufio"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestWrapLogin(t *testing.T) {
	argv := []string{"claude", "--resume", "abc 123"}
	cases := map[string][]string{
		"/bin/zsh":           {"/bin/zsh", "-l", "-c", `exec "$0" "$@"`, "claude", "--resume", "abc 123"},
		"/usr/bin/fish":      {"/usr/bin/fish", "-l", "-c", "exec $argv", "--", "claude", "--resume", "abc 123"},
		"/bin/tcsh":          {"/bin/sh", "-l", "-c", `exec "$0" "$@"`, "claude", "--resume", "abc 123"},
		"/opt/homebrew/bash": {"/opt/homebrew/bash", "-l", "-c", `exec "$0" "$@"`, "claude", "--resume", "abc 123"},
	}
	for shell, want := range cases {
		if got := wrapLogin(shell, argv); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %q", shell, got)
		}
	}
}

// TestWrapLoginPreservesArguments runs the wrapper for real to check quoting.
func TestWrapLoginPreservesArguments(t *testing.T) {
	argv := wrapLogin("/bin/sh", []string{"printf", "[%s]", "a b", `"q"`, "$HOME", "--flag"})
	out, err := exec.Command(argv[0], argv[1:]...).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != `[a b]["q"][$HOME][--flag]` {
		t.Fatalf("got %s", got)
	}
}

func TestPasswdShell(t *testing.T) {
	passwd := "root:x:0:0:root:/root:/bin/bash\nt:x:1000:1000::/home/t:/usr/bin/zsh\n"
	if got := passwdShell(bufio.NewScanner(strings.NewReader(passwd)), "t"); got != "/usr/bin/zsh" {
		t.Fatal(got)
	}
	if got := passwdShell(bufio.NewScanner(strings.NewReader(passwd)), "nobody"); got != "" {
		t.Fatal(got)
	}
}
