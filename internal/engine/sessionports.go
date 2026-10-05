package engine

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"skybuild/internal/model"
)

// Ports a session is listening on: a dev server started in a pane, by you or by Claude. The
// desktop app shows them under the pane so the server is one click away in the browser.

// sessionPortsScript prints tmux's panes, every process with its parent, and the listening
// sockets with their owners. Matching them up happens here, not on the machine.
const sessionPortsScript = `echo '@@panes@@'; tmux list-panes -a -F '#{session_name}	#{pane_pid}' 2>/dev/null
echo '@@procs@@'; ps -A -o pid=,ppid=,comm= 2>/dev/null
echo '@@listen@@'; if command -v ss >/dev/null 2>&1; then echo SS; ss -H -ltnp 2>/dev/null; else echo LSOF; lsof -nP -iTCP -sTCP:LISTEN -Fpn 2>/dev/null; fi; true`

var ssPid = regexp.MustCompile(`pid=(\d+)`)

// SessionPorts lists, per tmux session on a machine (LocalMachine: this computer), the TCP
// ports that programs started in it are listening on.
func (e *Engine) SessionPorts(ctx context.Context, machine string) (map[string][]model.Port, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := e.runOn(ctx, machine, sessionPortsScript)
	if err != nil {
		return nil, err
	}
	return parseSessionPorts(out), nil
}

func parseSessionPorts(out string) map[string][]model.Port {
	_, rest, _ := strings.Cut(out, "@@panes@@")
	panesRaw, rest, _ := strings.Cut(rest, "@@procs@@")
	procsRaw, listenRaw, _ := strings.Cut(rest, "@@listen@@")

	paneOf := map[int]string{} // a pane's shell → its session
	for _, line := range strings.Split(panesRaw, "\n") {
		name, pid, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if n, err := strconv.Atoi(pid); ok && err == nil {
			paneOf[n] = name
		}
	}
	parent := map[int]int{}
	comm := map[int]string{}
	for _, line := range strings.Split(procsRaw, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		parent[pid] = ppid
		name := strings.Join(f[2:], " ")
		comm[pid] = strings.TrimPrefix(name[strings.LastIndex(name, "/")+1:], "-")
	}
	sessionOf := func(pid int) string {
		for i := 0; i < 40 && pid > 1; i++ {
			if s, ok := paneOf[pid]; ok {
				return s
			}
			pid = parent[pid]
		}
		return ""
	}

	res := map[string][]model.Port{}
	seen := map[string]bool{}
	add := func(pid int, addr string) {
		i := strings.LastIndex(addr, ":")
		if i < 0 {
			return
		}
		port, err := strconv.Atoi(addr[i+1:])
		s := sessionOf(pid)
		if err != nil || port <= 0 || s == "" || seen[s+":"+strconv.Itoa(port)] {
			return
		}
		seen[s+":"+strconv.Itoa(port)] = true
		res[s] = append(res[s], model.Port{Port: port, Address: strings.Trim(addr[:i], "[]"), Process: comm[pid]})
	}
	lines := strings.Split(strings.TrimSpace(listenRaw), "\n")
	switch strings.TrimSpace(lines[0]) {
	case "SS": // LISTEN 0 511 0.0.0.0:3000 0.0.0.0:* users:(("node",pid=1234,fd=23))
		for _, line := range lines[1:] {
			f := strings.Fields(line)
			if len(f) < 4 {
				continue
			}
			for _, m := range ssPid.FindAllStringSubmatch(line, -1) {
				pid, _ := strconv.Atoi(m[1])
				add(pid, f[3])
			}
		}
	case "LSOF": // p1234 on one line, then n*:3000 for each of its sockets
		pid := 0
		for _, line := range lines[1:] {
			switch {
			case strings.HasPrefix(line, "p"):
				pid, _ = strconv.Atoi(line[1:])
			case strings.HasPrefix(line, "n") && pid > 0:
				add(pid, line[1:])
			}
		}
	}
	for s := range res {
		sort.Slice(res[s], func(i, j int) bool { return res[s][i].Port < res[s][j].Port })
	}
	return res
}
