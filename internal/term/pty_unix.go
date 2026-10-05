//go:build !windows

package term

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"

	"skybuild/internal/osx"
)

type unixProc struct {
	f   *os.File
	cmd *exec.Cmd
}

func start(s Spec) (proc, error) {
	cmd := exec.Command(osx.Which(s.Args[0]), s.Args[1:]...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor", "TERM_PROGRAM=Lungo")
	cmd.Env = append(cmd.Env, s.Env...)
	cmd.Dir = s.Dir
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(s.Cols), Rows: uint16(s.Rows)})
	if err != nil {
		return nil, err
	}
	return &unixProc{f: f, cmd: cmd}, nil
}

func (p *unixProc) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p *unixProc) Write(b []byte) (int, error) { return p.f.Write(b) }

func (p *unixProc) Resize(cols, rows int) error {
	return pty.Setsize(p.f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

func (p *unixProc) Wait() (int, error) {
	err := p.cmd.Wait()
	if p.cmd.ProcessState != nil {
		return p.cmd.ProcessState.ExitCode(), err
	}
	return -1, err
}

func (p *unixProc) Kill() {
	if p.cmd.Process != nil {
		p.cmd.Process.Signal(syscall.SIGHUP)
		p.cmd.Process.Kill()
	}
}

func (p *unixProc) Close() error { return p.f.Close() }

func (p *unixProc) Pid() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}
