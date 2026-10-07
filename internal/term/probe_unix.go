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

	procs "skybuild/internal/proc"
)

// probe reads the process tree under a terminal's shell. The program "in front" is the
// leader of the terminal's foreground process group (the shell itself at an idle prompt).
// The table comes from the system directly (see package proc), command lines only for this tree.
func probe(shell int) Probe {
	if shell <= 0 {
		return Probe{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	t := procs.List(ctx)
	root, ok := t.Rows[shell]
	if !ok {
		return Probe{}
	}
	p := Probe{}
	front := root
	var walk func(pid, depth int)
	walk = func(pid, depth int) {
		r := t.Rows[pid]
		if isClaude(t.Args(pid)) {
			p.Claude = true
		}
		if r.PID == root.TPGID {
			front = r
		}
		if depth > 12 {
			return
		}
		for _, k := range t.Kids[pid] {
			walk(k, depth+1)
		}
	}
	walk(shell, 0)
	args := t.Args(front.PID)
	p.Command = commandName(args)
	if isClaude(args) {
		p.Command = "claude"
	}
	p.Cwd = cwdOf(ctx, front.PID)
	if p.Cwd == "" && front.PID != shell {
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
