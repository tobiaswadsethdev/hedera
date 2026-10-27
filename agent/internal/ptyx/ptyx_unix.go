//go:build !windows

package ptyx

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"

	pty "github.com/aymanbagabas/go-pty"
	"golang.org/x/sys/unix"
)

type unixProcess struct {
	pty pty.Pty
	cmd *pty.Cmd

	closeOnce sync.Once
}

// Start starts o.Argv on a new PTY of the requested size.
func Start(o Options) (Process, error) {
	if len(o.Argv) == 0 {
		return nil, errors.New("ptyx: empty argv")
	}
	p, err := pty.New()
	if err != nil {
		return nil, err
	}
	if err := p.Resize(o.Cols, o.Rows); err != nil {
		p.Close()
		return nil, err
	}
	path := o.Argv[0]
	if lp, err := exec.LookPath(path); err == nil {
		path = lp
	} else {
		p.Close()
		return nil, err
	}
	cmd := p.Command(path, o.Argv[1:]...)
	cmd.Dir = o.Dir
	cmd.Env = o.Env
	if err := cmd.Start(); err != nil {
		p.Close()
		return nil, err
	}
	// Drop our copy of the terminal side so Read reports EOF/EIO once the
	// child and everything it spawned have closed theirs.
	if up, ok := p.(pty.UnixPty); ok {
		up.Slave().Close()
	}
	return &unixProcess{pty: p, cmd: cmd}, nil
}

func (u *unixProcess) Read(b []byte) (int, error)  { return u.pty.Read(b) }
func (u *unixProcess) Write(b []byte) (int, error) { return u.pty.Write(b) }
func (u *unixProcess) Pid() int                    { return u.cmd.Process.Pid }

func (u *unixProcess) Resize(cols, rows int) error { return u.pty.Resize(cols, rows) }

func (u *unixProcess) Wait() ExitStatus {
	u.cmd.Wait()
	var st ExitStatus
	ps := u.cmd.ProcessState
	if ps == nil {
		return st
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		st.Signal = unix.SignalName(ws.Signal())
		return st
	}
	code := ps.ExitCode()
	st.Code = &code
	return st
}

// The child is a session leader (setsid), so its pid is also its process
// group ID; signalling -pid reaches its whole foreground job tree.
func (u *unixProcess) signal(sig syscall.Signal) error {
	pid := u.cmd.Process.Pid
	err := syscall.Kill(-pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		err = syscall.Kill(pid, sig)
	}
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func (u *unixProcess) Hangup() error { return u.signal(syscall.SIGHUP) }
func (u *unixProcess) Kill() error   { return u.signal(syscall.SIGKILL) }

func (u *unixProcess) Close() error {
	var err error
	u.closeOnce.Do(func() {
		// The slave was already closed in Start; only the master error matters.
		if up, ok := u.pty.(pty.UnixPty); ok {
			err = up.Master().Close()
			return
		}
		err = u.pty.Close()
	})
	return err
}
