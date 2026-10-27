package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/tobiaswadsethdev/hedera/agent/internal/hooks"
	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
	"github.com/tobiaswadsethdev/hedera/agent/internal/service"
)

func cmdInstallService(args []string) error {
	fs := newFlags("install-service", usageFor("install-service"))
	uninstall := fs.Bool("uninstall", false, "stop the service and remove it")
	printOnly := fs.Bool("print", false, "print the service file instead of installing it")
	force := fs.Bool("force", false, "install even though sessions are running (restarting the agent ends them)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	exe, err := stableExecutable()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	unit, err := service.Plan(exe, home)
	if err != nil {
		return err
	}
	if *printOnly {
		fmt.Printf("# %s\n%s", unit.Path, unit.Content)
		return nil
	}
	if !*force {
		if n := runningSessions(); n > 0 {
			return fmt.Errorf("%d session(s) are running and would be ended by restarting the agent; close them or pass --force", n)
		}
	}
	if *uninstall {
		if err := service.Uninstall(unit); err != nil {
			return err
		}
		fmt.Printf("removed %s\n", unit.Path)
		return nil
	}
	notes, err := service.Install(unit)
	if err != nil {
		return err
	}
	fmt.Printf("installed %s; hedera now starts at login\n", unit.Path)
	for _, n := range notes {
		fmt.Println(n)
	}
	return nil
}

// runningSessions asks the local agent how many sessions are alive; 0 when
// it isn't running.
func runningSessions() int {
	c, err := dial("")
	if err != nil {
		return 0
	}
	defer c.Close()
	ctx, cancel := callCtx()
	defer cancel()
	var res proto.ListResult
	if c.Call(ctx, proto.TSessionList, nil, &res) != nil {
		return 0
	}
	n := 0
	for _, s := range res.Sessions {
		if s.Exit == nil {
			n++
		}
	}
	return n
}

// stableExecutable is this binary's resolved path, refusing temporary
// `go run` builds that would vanish and leave services or hooks dangling.
func stableExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}
	if strings.HasPrefix(exe, os.TempDir()) || strings.Contains(exe, "go-build") {
		return "", fmt.Errorf("%s looks like a temporary build; install the binary somewhere permanent first (e.g. go install, or copy it to ~/.local/bin)", exe)
	}
	return exe, nil
}

func cmdInstallHooks(args []string) error {
	fs := newFlags("install-hooks", usageFor("install-hooks"))
	dryRun := fs.Bool("dry-run", false, "show the change without writing it")
	uninstall := fs.Bool("uninstall", false, "remove hedera's hooks")
	settings := fs.String("settings", "", "settings file to edit (default: $CLAUDE_CONFIG_DIR/settings.json or ~/.claude/settings.json)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := *settings
	if path == "" {
		var err error
		if path, err = hooks.SettingsPath(); err != nil {
			return err
		}
	}
	edit := hooks.Uninstall
	if !*uninstall {
		exe, err := stableExecutable()
		if err != nil {
			return err
		}
		inv := hooks.InvocationFor(exe, runtime.GOOS)
		edit = func(v *hooks.Value) error { return hooks.Install(v, inv) }
	}
	plan, err := hooks.Prepare(path, edit)
	if err != nil {
		return err
	}
	if !plan.Changed() {
		if *uninstall {
			fmt.Printf("no hedera hooks in %s\n", path)
		} else {
			fmt.Printf("hooks already up to date in %s\n", path)
		}
		return warnDisabled(plan)
	}
	if *dryRun {
		fmt.Printf("would change %s (formatting normalized to 2-space indentation):\n\n%s", path, plan.Diff())
		return warnDisabled(plan)
	}
	backup, err := plan.Apply(time.Now())
	if err != nil {
		return err
	}
	if backup != "" {
		fmt.Printf("backed up the previous file to %s\n", backup)
	}
	if *uninstall {
		fmt.Printf("removed hedera's hooks from %s\n", path)
		return nil
	}
	fmt.Printf("installed hedera's hooks in %s\n", path)
	fmt.Println("Claude Code sessions started inside hedera now report their state.")
	fmt.Println("Already-running Claude Code sessions may need a restart to pick the hooks up.")
	fmt.Println("Note: Claude Code holds hooks back in a folder until you accept its trust dialog.")
	return warnDisabled(plan)
}

func warnDisabled(p *hooks.Plan) error {
	if p.Disabled {
		fmt.Fprintln(os.Stderr, "warning: disableAllHooks is set in this file, so Claude Code runs no hooks at all")
	}
	return nil
}
