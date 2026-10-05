//go:build windows

package term

import (
	"context"
	"os"
	"strings"

	"github.com/UserExistsError/conpty"

	"skybuild/internal/osx"
)

type winProc struct{ c *conpty.ConPty }

func start(s Spec) (proc, error) {
	args := append([]string{osx.Which(s.Args[0])}, s.Args[1:]...)
	for i, a := range args {
		if strings.ContainsAny(a, " \t\"") {
			args[i] = `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
		}
	}
	opts := []conpty.ConPtyOption{
		conpty.ConPtyDimensions(s.Cols, s.Rows),
		conpty.ConPtyEnv(append(append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor"), s.Env...)),
	}
	if s.Dir != "" {
		opts = append(opts, conpty.ConPtyWorkDir(s.Dir))
	}
	c, err := conpty.Start(strings.Join(args, " "), opts...)
	if err != nil {
		return nil, err
	}
	return &winProc{c: c}, nil
}

func (p *winProc) Read(b []byte) (int, error)  { return p.c.Read(b) }
func (p *winProc) Write(b []byte) (int, error) { return p.c.Write(b) }
func (p *winProc) Resize(cols, rows int) error { return p.c.Resize(cols, rows) }

func (p *winProc) Wait() (int, error) {
	code, err := p.c.Wait(context.Background())
	return int(code), err
}

func (p *winProc) Kill() {
	if proc, err := os.FindProcess(p.c.Pid()); err == nil {
		proc.Kill()
	}
}

func (p *winProc) Close() error { return p.c.Close() }

func (p *winProc) Pid() int { return p.c.Pid() }

// probe has no cheap equivalent on Windows; panes there keep their static names.
func probe(int) Probe { return Probe{} }
