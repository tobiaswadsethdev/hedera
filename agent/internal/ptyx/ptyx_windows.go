//go:build windows

package ptyx

import (
	"errors"
	"os/exec"
	"sync"

	pty "github.com/aymanbagabas/go-pty"
)

// Windows support is compile-only until Phase 5: hangup is a hard kill and
// child processes are not yet tracked with a job object.
type winProcess struct {
	pty pty.Pty
	cmd *pty.Cmd

	closeOnce sync.Once
}

func Start(o Options) (Process, error) {
	if len(o.Argv) == 0 {
		return nil, errors.New("ptyx: empty argv")
	}
	path, err := exec.LookPath(o.Argv[0])
	if err != nil {
		return nil, err
	}
	p, err := pty.New()
	if err != nil {
		return nil, err
	}
	if err := p.Resize(o.Cols, o.Rows); err != nil {
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
	return &winProcess{pty: p, cmd: cmd}, nil
}

func (w *winProcess) Read(b []byte) (int, error)  { return w.pty.Read(b) }
func (w *winProcess) Write(b []byte) (int, error) { return w.pty.Write(b) }
func (w *winProcess) Pid() int                    { return w.cmd.Process.Pid }
func (w *winProcess) Resize(cols, rows int) error { return w.pty.Resize(cols, rows) }

func (w *winProcess) Wait() ExitStatus {
	w.cmd.Wait()
	var st ExitStatus
	if ps := w.cmd.ProcessState; ps != nil {
		code := ps.ExitCode()
		st.Code = &code
	}
	return st
}

func (w *winProcess) Hangup() error { return w.cmd.Process.Kill() }
func (w *winProcess) Kill() error   { return w.cmd.Process.Kill() }

func (w *winProcess) Close() error {
	var err error
	w.closeOnce.Do(func() { err = w.pty.Close() })
	return err
}
