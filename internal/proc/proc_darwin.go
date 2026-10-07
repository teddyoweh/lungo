//go:build darwin

package proc

import (
	"bytes"
	"context"
	"encoding/binary"
	"strings"

	"golang.org/x/sys/unix"
)

// List asks the system for every process: under a millisecond, where `ps -A` with command
// lines took 400ms on a Mac running a thousand processes.
func List(ctx context.Context) *Table {
	list, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return psList(ctx)
	}
	t := newTable(len(list))
	t.read = args
	for i := range list {
		p := &list[i]
		if pid := int(p.Proc.P_pid); pid > 0 {
			t.add(Row{PID: pid, PPID: int(p.Eproc.Ppid), PGID: int(p.Eproc.Pgid), TPGID: int(p.Eproc.Tpgid)})
		}
	}
	return t
}

// args is a process's command line, as ps shows it (its arguments joined by spaces). The
// system hands it over as argc, the program's path, padding, then the arguments.
func args(pid int) string {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(b) < 4 {
		return ""
	}
	argc := int(binary.LittleEndian.Uint32(b[:4]))
	b = b[4:]
	i := bytes.IndexByte(b, 0)
	if i < 0 {
		return ""
	}
	b = bytes.TrimLeft(b[i:], "\x00")
	out := make([]string, 0, argc)
	for len(out) < argc && len(b) > 0 {
		j := bytes.IndexByte(b, 0)
		if j < 0 {
			out = append(out, string(b))
			break
		}
		out = append(out, string(b[:j]))
		b = b[j+1:]
	}
	return strings.Join(out, " ")
}
