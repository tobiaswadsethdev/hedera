//go:build windows

package ptyx

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"unsafe"

	pty "github.com/aymanbagabas/go-pty"
	"golang.org/x/sys/windows"
)

// winProcess is a process on a ConPTY, inside a job object so the whole
// process tree can be terminated together (Windows has no process groups).
//
// Hangup closes the pseudo console, which sends CTRL_CLOSE_EVENT to every
// attached process: the Windows counterpart of a terminal hanging up. Kill
// terminates the job. The job is created with KILL_ON_JOB_CLOSE, so if the
// agent dies, its sessions die with it, as they do on Unix.
type winProcess struct {
	pty pty.Pty
	cmd *pty.Cmd
	job windows.Handle

	closeOnce sync.Once
	jobOnce   sync.Once
}

func Start(o Options) (Process, error) {
	if len(o.Argv) == 0 {
		return nil, errors.New("ptyx: empty argv")
	}
	path, err := exec.LookPath(o.Argv[0])
	if err != nil {
		return nil, err
	}
	job, err := newKillOnCloseJob()
	if err != nil {
		return nil, err
	}
	p, err := pty.New()
	if err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	if err := p.Resize(o.Cols, o.Rows); err != nil {
		p.Close()
		windows.CloseHandle(job)
		return nil, err
	}
	cmd := p.Command(path, o.Argv[1:]...)
	cmd.Dir = o.Dir
	cmd.Env = o.Env
	if err := cmd.Start(); err != nil {
		p.Close()
		windows.CloseHandle(job)
		return nil, err
	}
	// go-pty doesn't expose the main thread handle, so the process can't be
	// created suspended; it joins the job right after it starts. A child it
	// spawned in those first microseconds would escape the job, which only
	// matters for Kill (Hangup reaches everything on the console anyway).
	if h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid)); err == nil {
		windows.AssignProcessToJobObject(job, h)
		windows.CloseHandle(h)
	}
	return &winProcess{pty: p, cmd: cmd, job: job}, nil
}

func newKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
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
	// Anything the process left running dies with the job.
	w.closeJob()
	return st
}

func (w *winProcess) Hangup() error {
	if w.cmd.ProcessState != nil {
		return os.ErrProcessDone
	}
	return w.Close()
}

func (w *winProcess) Kill() error {
	if w.cmd.ProcessState != nil {
		return os.ErrProcessDone
	}
	if err := windows.TerminateJobObject(w.job, 1); err != nil {
		// Not in the job (see Start): fall back to the process itself.
		return w.cmd.Process.Kill()
	}
	return nil
}

func (w *winProcess) closeJob() {
	w.jobOnce.Do(func() { windows.CloseHandle(w.job) })
}

func (w *winProcess) Close() error {
	var err error
	w.closeOnce.Do(func() { err = w.pty.Close() })
	return err
}
