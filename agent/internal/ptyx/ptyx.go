// Package ptyx starts processes on a pseudo-terminal and hides the platform
// differences (Unix PTY vs. Windows ConPTY) behind Process.
package ptyx

import "io"

// Options describes a process to start.
type Options struct {
	Argv []string // Argv[0] is resolved with the agent's PATH if it has no separator.
	Dir  string
	Env  []string
	Cols int
	Rows int
}

// ExitStatus describes how a process ended. Code is nil when the process was
// killed by a signal; Signal is then its name ("SIGHUP"), otherwise "".
type ExitStatus struct {
	Code   *int
	Signal string
}

// Process is a running process attached to a PTY.
//
// Read returns output until the PTY is closed or every holder of the terminal
// has exited. Wait blocks until the process exits and must be called once.
// Close releases the PTY and unblocks any pending Read.
type Process interface {
	io.Reader
	io.Writer
	Pid() int
	Resize(cols, rows int) error
	Wait() ExitStatus
	// Hangup asks the process (group) to terminate, like a terminal closing.
	Hangup() error
	// Kill terminates the process (group) immediately.
	Kill() error
	Close() error
}
