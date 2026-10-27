package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	if strings.HasPrefix(exe, os.TempDir()) || strings.Contains(exe, "go-build") {
		return fmt.Errorf("%s looks like a temporary build; install the binary somewhere permanent first (e.g. go install, or copy it to ~/.local/bin)", exe)
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

func cmdInstallHooks([]string) error {
	return errors.New("Claude Code hook integration arrives in Phase 2")
}
