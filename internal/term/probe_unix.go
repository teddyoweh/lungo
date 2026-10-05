//go:build !windows

package term

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type psRow struct {
	pid, ppid, pgid, tpgid int
	args                   string
}

// probe reads the process tree under a terminal's shell. The program "in front" is the
// leader of the terminal's foreground process group (the shell itself at an idle prompt).
func probe(shell int) Probe {
	if shell <= 0 {
		return Probe{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=,ppid=,pgid=,tpgid=,args=").Output()
	if err != nil {
		return Probe{}
	}
	rows := map[int]psRow{}
	kids := map[int][]int{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		var r psRow
		r.pid, _ = strconv.Atoi(f[0])
		r.ppid, _ = strconv.Atoi(f[1])
		r.pgid, _ = strconv.Atoi(f[2])
		r.tpgid, _ = strconv.Atoi(f[3])
		r.args = strings.Join(f[4:], " ")
		rows[r.pid] = r
		kids[r.ppid] = append(kids[r.ppid], r.pid)
	}
	root, ok := rows[shell]
	if !ok {
		return Probe{}
	}
	p := Probe{}
	front := root
	var walk func(pid, depth int)
	walk = func(pid, depth int) {
		r := rows[pid]
		if isClaude(r.args) {
			p.Claude = true
		}
		if r.pid == root.tpgid {
			front = r
		}
		if depth > 12 {
			return
		}
		for _, k := range kids[pid] {
			walk(k, depth+1)
		}
	}
	walk(shell, 0)
	p.Command = commandName(front.args)
	if isClaude(front.args) {
		p.Command = "claude"
	}
	p.Cwd = cwdOf(ctx, front.pid)
	if p.Cwd == "" && front.pid != shell {
		p.Cwd = cwdOf(ctx, shell)
	}
	return p
}

// isClaude recognises Claude Code in a process's command line: run by name, or as the
// versioned binary its installer keeps under …/claude/versions/.
func isClaude(args string) bool {
	arg0, _, _ := strings.Cut(args, " ")
	return filepath.Base(arg0) == "claude" || strings.Contains(arg0, "/claude/versions/")
}

func commandName(args string) string {
	arg0, _, _ := strings.Cut(args, " ")
	return strings.TrimPrefix(filepath.Base(arg0), "-") // login shells show as -zsh
}

func cwdOf(ctx context.Context, pid int) string {
	if runtime.GOOS == "linux" {
		dir, _ := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd")
		return dir
	}
	out, err := exec.CommandContext(ctx, "lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			return line[1:]
		}
	}
	return ""
}
