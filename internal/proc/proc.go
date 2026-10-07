// Package proc reads the processes running on this computer: who is whose parent, which
// process group each is in, and (when asked) a process's command line.
package proc

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
)

// Row is one process.
type Row struct {
	PID, PPID int
	PGID      int // its process group
	TPGID     int // the foreground process group of its terminal (the program "in front")
}

// Table is every process, read at one moment.
type Table struct {
	Rows map[int]Row
	Kids map[int][]int // parent → children
	seen map[int]string
	read func(pid int) string
}

// Args is a process's command line as ps shows it ("" when it can't be read), read when
// first asked: reading every process's would cost far more than the few that are needed.
func (t *Table) Args(pid int) string {
	if a, ok := t.seen[pid]; ok {
		return a
	}
	a := ""
	if t.read != nil {
		a = t.read(pid)
	}
	t.seen[pid] = a
	return a
}

// Known is a table made from known command lines and parents (tests, or a list read
// elsewhere).
func Known(args map[int]string, kids map[int][]int) *Table {
	t := newTable(len(args))
	for pid, a := range args {
		t.Rows[pid] = Row{PID: pid}
		t.seen[pid] = a
	}
	t.Kids = kids
	return t
}

func newTable(n int) *Table {
	return &Table{Rows: make(map[int]Row, n), Kids: make(map[int][]int, n), seen: map[int]string{}}
}

func (t *Table) add(r Row) {
	t.Rows[r.PID] = r
	t.Kids[r.PPID] = append(t.Kids[r.PPID], r.PID)
}

// psList reads the table with ps: where the system can't be asked directly.
func psList(ctx context.Context) *Table {
	t := newTable(512)
	out, err := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=,ppid=,pgid=,tpgid=,args=").Output()
	if err != nil {
		return t
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		var r Row
		r.PID, _ = strconv.Atoi(f[0])
		r.PPID, _ = strconv.Atoi(f[1])
		r.PGID, _ = strconv.Atoi(f[2])
		r.TPGID, _ = strconv.Atoi(f[3])
		t.add(r)
		t.seen[r.PID] = strings.Join(f[4:], " ")
	}
	return t
}
