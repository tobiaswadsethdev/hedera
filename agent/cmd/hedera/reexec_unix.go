//go:build !windows

package main

import (
	"os"
	"syscall"
)

// reexec replaces this process with exe, keeping its arguments, environment
// and PID, so launchd and systemd see the same service carry on.
func reexec(exe string) error {
	return syscall.Exec(exe, os.Args, os.Environ())
}
