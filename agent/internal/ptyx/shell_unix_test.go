//go:build !windows

package ptyx

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWrapLogin(t *testing.T) {
	argv := []string{"claude", "--resume", "abc 123"}
	cases := map[string][]string{
		"/bin/zsh":           {"/bin/zsh", "-l", "-i", "-c", `exec "$0" "$@"`, "claude", "--resume", "abc 123"},
		"/usr/bin/fish":      {"/usr/bin/fish", "-l", "-i", "-c", "exec $argv", "--", "claude", "--resume", "abc 123"},
		"/bin/tcsh":          {"/bin/sh", "-l", "-i", "-c", `exec "$0" "$@"`, "claude", "--resume", "abc 123"},
		"/opt/homebrew/bash": {"/opt/homebrew/bash", "-l", "-i", "-c", `exec "$0" "$@"`, "claude", "--resume", "abc 123"},
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

// TestWrapLoginReadsInteractiveRC checks that a command found only through
// PATH set in ~/.bashrc after the usual "return unless interactive" guard
// still starts, as it would when typed into a terminal.
func TestWrapLoginReadsInteractiveRC(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	home := t.TempDir()
	files := map[string]string{
		".bash_profile":   "[[ -f ~/.bashrc ]] && . ~/.bashrc\n",
		".bashrc":         "[[ $- != *i* ]] && return\nexport PATH=\"$HOME/bin:$PATH\"\n",
		"bin/hedera-tool": "#!/bin/sh\necho tool-ran\n",
	}
	for name, body := range files {
		path := filepath.Join(home, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p, err := Start(Options{
		Argv: wrapLogin(bash, []string{"hedera-tool"}),
		Dir:  home,
		Env:  []string{"HOME=" + home, "PATH=/usr/bin:/bin", "TERM=dumb"},
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	out, _ := io.ReadAll(p) // ends with EIO once the child exits
	st := p.Wait()
	if !strings.Contains(string(out), "tool-ran") || st.Code == nil || *st.Code != 0 {
		t.Fatalf("exit %+v, output %q", st, out)
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
