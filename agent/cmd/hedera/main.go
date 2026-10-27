// Command hedera is the Hedera agent and its command-line client.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = "dev"

type command struct {
	name  string
	args  string
	short string
	run   func(args []string) error
}

var commands []command

func init() {
	commands = []command{
		{"run", "[--dev-loopback] [-v] [--log-format text|json]", "run the agent in the foreground", cmdRun},
		{"pair", "[--no-qr]", "show a QR code to pair the Hedera app (or another hedera)", cmdPair},
		{"tokens", "[list | revoke <pairing-id>]", "list or revoke paired devices", cmdTokens},
		{"ls", "[--json]", "list sessions", cmdList},
		{"new", "[--claude] [--cwd DIR] [--title T] [-d] [-- COMMAND ARGS…]", "start a session and attach to it", cmdNew},
		{"resume", "[<claude-session-id>] [-d | --forget]", "list or resume Claude sessions lost when the agent stopped", cmdResume},
		{"attach", "<session-id> [--from OFFSET]", "attach this terminal to a session (Ctrl-] detaches)", cmdAttach},
		{"send", "<session-id> <text> [--enter] [--if-turn N]", "type text into a session, like a notification reply", cmdSend},
		{"close", "<session-id> [--kill]", "hang up (or kill) a session", cmdClose},
		{"rm", "<session-id>", "remove an exited session and its scrollback", cmdRemove},
		{"events", "[--since SEQ]", "print the event stream as JSON lines", cmdEvents},
		{"remote", "add <hedera://pair…> [--name N] | ls | rm <name>", "pair this CLI with another machine's agent", cmdRemote},
		{"push", "[status | test | configure --radix URL --token T | remove]", "push notification routes through Radix", cmdPush},
		{"install-service", "[--uninstall] [--print] [--force]", "run hedera at login (launchd or systemd --user)", cmdInstallService},
		{"install-hooks", "[--dry-run] [--uninstall] [--settings PATH]", "add hedera's hooks to Claude Code's settings.json", cmdInstallHooks},
		{"hook", "", "Claude Code hook handler (run by Claude Code, not by hand)", cmdHook},
		{"version", "", "print the version", func([]string) error {
			fmt.Printf("hedera %s (protocol %d)\n", version, proto.Version)
			return nil
		}},
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: hedera <command> [flags]\n\n")
	for _, c := range commands {
		fmt.Fprintf(os.Stderr, "  %-16s %s\n", c.name, c.short)
	}
	fmt.Fprintf(os.Stderr, "\nClient commands (ls, new, attach, close, rm, events) talk to the agent on this\n"+
		"machine, or to a paired one with --remote NAME. Run 'hedera <command> -h' for flags.\n")
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	name := os.Args[1]
	if name == "hook" {
		// Hook handlers must exit 0 and stay silent whatever happens.
		cmdHook(os.Args[2:])
		os.Exit(0)
	}
	if name == "-h" || name == "--help" || name == "help" {
		usage()
		return
	}
	for _, c := range commands {
		if c.name == name {
			err := c.run(os.Args[2:])
			if errors.Is(err, flag.ErrHelp) {
				return
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "hedera %s: %v\n", name, err)
				os.Exit(1)
			}
			return
		}
	}
	fmt.Fprintf(os.Stderr, "hedera: unknown command %q\n\n", name)
	usage()
	os.Exit(2)
}

func newFlags(name, args string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: hedera %s %s\n", name, args)
		fs.PrintDefaults()
	}
	return fs
}

func usageFor(name string) string {
	for _, c := range commands {
		if c.name == name {
			return c.args
		}
	}
	return ""
}

// parseInterspersed parses flags that may come before or after positional
// arguments (Go's flag package stops at the first positional one). Arguments
// after "--" are returned separately and never parsed.
func parseInterspersed(fs *flag.FlagSet, args []string) (positional, rest []string, err error) {
	for i, a := range args {
		if a == "--" {
			rest = args[i+1:]
			args = args[:i]
			break
		}
	}
	for {
		if err := fs.Parse(args); err != nil {
			return nil, nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, rest, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}
