package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"skybuild/internal/bootstrap"
	"skybuild/internal/osx"
	"skybuild/internal/paths"
	"skybuild/internal/sshx"
)

// Sessions on this computer.
//
// They run in a tmux server of sky's own (its own socket and settings, apart from any tmux
// the user runs), so a local pane is a session exactly like one on a machine: it keeps
// running when the app quits and is there again when the app comes back. Where a function
// takes a machine name, LocalMachine means this computer.

// LocalMachine is the machine name that sessions on this computer carry. It can't be a real
// machine's name (those are letters, digits and dashes).
const LocalMachine = "@local"

// IsLocal reports whether a machine name means this computer.
func IsLocal(machine string) bool { return machine == LocalMachine }

// ErrNoLocalTmux: sessions on this computer need tmux, and it isn't installed.
var ErrNoLocalTmux = errors.New("tmux isn't installed on this computer")

// localSocket names sky's tmux server. SKY_TMUX_SOCKET gives a development build its own,
// so it never touches the sessions of the installed app.
func localSocket() string {
	if s := os.Getenv("SKY_TMUX_SOCKET"); s != "" {
		return s
	}
	return "skybuild"
}

// LocalTmux is the path of tmux on this computer, or "" when it isn't installed.
func LocalTmux() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	if p := osx.Which("tmux"); filepath.IsAbs(p) {
		return p
	}
	if p, err := exec.LookPath("tmux"); err == nil {
		return p
	}
	return ""
}

// localTmuxConf is what sky's tmux server on this computer starts with: the settings every
// machine gets, no status line (the app shows what it would), and a terminal type every
// program on macOS knows.
const localTmuxConf = `# skybuild: tmux settings for sessions on this computer. Rewritten by the app.
set -g default-terminal "xterm-256color"
set -g status off
set -g base-index 1
setw -g pane-base-index 1
` + bootstrap.TmuxManaged

func localConf() (string, error) {
	path := filepath.Join(paths.Root(), "tmux.local.conf")
	if have, err := os.ReadFile(path); err == nil && string(have) == localTmuxConf {
		return path, nil
	}
	if err := os.MkdirAll(paths.Root(), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(localTmuxConf), 0o600); err != nil {
		return "", err
	}
	// New settings reach a server that is already running (it read the file when it started).
	if tm := LocalTmux(); tm != "" {
		_ = exec.Command(tm, "-L", localSocket(), "source-file", path).Run()
	}
	return path, nil
}

// localPrelude makes `tmux` in a script mean sky's server on this computer. The variables
// it drops belong to a Claude Code session the app may have been started from; a session
// in a pane must not inherit them.
func localPrelude() (string, error) {
	tm := LocalTmux()
	if tm == "" {
		return "", ErrNoLocalTmux
	}
	conf, err := localConf()
	if err != nil {
		return "", err
	}
	return "unset CLAUDECODE CLAUDE_CODE_ENTRYPOINT CLAUDE_CODE_SSE_PORT; tmux() { " + sshx.Quote(tm) + " -L " + sshx.Quote(localSocket()) + " -f " + sshx.Quote(conf) + ` "$@"; }; `, nil
}

// localSh runs a script on this computer with `tmux` meaning sky's server.
func localSh(ctx context.Context, script string) (string, error) {
	pre, err := localPrelude()
	if err != nil {
		return "", err
	}
	r, err := osx.Exec(ctx, osx.Cmd{Name: "/bin/sh", Args: []string{"-c", pre + script}, Dir: paths.Home()})
	return r.Stdout, err
}

// runOn runs a script on a machine, or on this computer for LocalMachine.
func (e *Engine) runOn(ctx context.Context, machine, script string) (string, error) {
	if IsLocal(machine) {
		return localSh(ctx, script)
	}
	m, err := e.Machine(machine)
	if err != nil {
		return "", err
	}
	return sshx.Run(ctx, e.Target(m), script)
}

// LocalAttachArgs is the command that opens a session on this computer in a terminal,
// creating it first when it doesn't exist (see AttachArgsIn).
func (e *Engine) LocalAttachArgs(session string, o AttachOptions) ([]string, error) {
	pre, err := localPrelude()
	if err != nil {
		return nil, err
	}
	if session == "" {
		return nil, errors.New("a session name is needed")
	}
	// No exec at the end: `tmux` is a shell function here.
	return []string{"/bin/sh", "-c", pre + attachScriptWith("tmux", session, o)}, nil
}

const localListCmd = `tmux list-sessions -F '#{session_name}	#{session_created}	#{window_activity}	#{session_attached}	#{session_windows}	#{pane_current_path}	#{pane_current_command}	#{host_short}	#{pane_title}	#{?mouse_any_flag,m,}#{?alternate_on,a,}#{?@sky-keys,k,}' 2>/dev/null; %s echo '@@sky-panes@@'; tmux list-panes -a -F '#{session_name}	#{pane_pid}	#{window_active}#{pane_active}' 2>/dev/null; true`

// LocalSessions lists the sessions on this computer. Claude's state comes from Claude Code's
// own record of its running sessions (no hooks are installed here) and from the screen.
func (e *Engine) LocalSessions(ctx context.Context) ([]Session, error) {
	if LocalTmux() == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	// Branches only where git can be run without side effects (see localGit).
	git := ""
	if localGit() {
		git = branchesCmd
	}
	out, err := localSh(ctx, fmt.Sprintf(localListCmd, git))
	if err != nil {
		return nil, err
	}
	list, panesRaw, _ := strings.Cut(out, "@@sky-panes@@")
	sessions := parseSessions(LocalMachine, list)
	if len(sessions) == 0 {
		return sessions, nil
	}
	shells := map[string]int{} // session → pid of the shell in its active pane
	for _, line := range strings.Split(panesRaw, "\n") {
		f := strings.Split(strings.TrimSpace(line), "\t")
		if len(f) < 3 {
			continue
		}
		if pid, err := strconv.Atoi(f[1]); err == nil && (f[2] == "11" || shells[f[0]] == 0) {
			shells[f[0]] = pid
		}
	}
	procs := processTable(ctx)
	var capture strings.Builder
	claudes := map[string]int{}
	others := map[string]string{} // session → another agent running in it
	for i := range sessions {
		s := &sessions[i]
		pid := procs.claudeUnder(shells[s.Name])
		if pid == 0 {
			s.State, s.Message = "", ""
			s.Claude = strings.HasPrefix(s.Name, "claude") // Claude has exited; a shell is in the pane now
			if a := procs.agentUnder(shells[s.Name]); a != "" {
				s.Claude, s.Agent, s.Command, s.State = false, a, a, "idle"
				s.Title = agentTitle(a, s.Title, s.Path)
				others[s.Name] = a
				capture.WriteString("echo " + sshx.Quote("@@sky-pane@@"+s.Name) + "; tmux capture-pane -p -t " + sshx.Quote("="+s.Name+":") + " 2>/dev/null | grep -v '^[[:space:]]*$' | tail -30; ")
			}
			continue
		}
		s.Agent = "claude"
		claudes[s.Name] = pid
		s.Claude, s.Command = true, "claude"
		s.Flags = ClaudeFlags(procs.args[pid])
		capture.WriteString("echo " + sshx.Quote("@@sky-pane@@"+s.Name) + "; tmux capture-pane -p -t " + sshx.Quote("="+s.Name+":") + " 2>/dev/null | grep -v '^[[:space:]]*$' | tail -30; ")
	}
	if len(claudes) == 0 && len(others) == 0 {
		return sessions, nil
	}
	screens := map[string]string{}
	if raw, err := localSh(ctx, capture.String()+"true"); err == nil {
		for _, chunk := range strings.Split(raw, "@@sky-pane@@") {
			if name, body, ok := strings.Cut(chunk, "\n"); ok {
				screens[strings.TrimSpace(name)] = body
			}
		}
	}
	host, _ := os.Hostname()
	host, _, _ = strings.Cut(host, ".")
	for i := range sessions {
		s := &sessions[i]
		if a, ok := others[s.Name]; ok {
			if state, msg := agentScreenState(a, screens[s.Name]); state != "" {
				s.State, s.Message = state, msg
			}
			continue
		}
		pid, ok := claudes[s.Name]
		if !ok {
			continue
		}
		rec := readClaudeRecord(pid)
		s.SID = rec.SessionID
		switch rec.Status {
		case "busy", "shell":
			s.State = "working"
		case "idle":
			s.State = "idle"
		default:
			s.State = ""
		}
		if rec.StatusUpdatedAt > 0 {
			s.StateAt = time.UnixMilli(rec.StatusUpdatedAt)
		}
		if s.Title == "" {
			s.Title = cleanTitle(rec.Name, host)
		}
		// A prompt on screen is the surest sign Claude is waiting on you.
		if state, msg := screenState(screens[s.Name]); state == "waiting" || (state != "" && s.State == "") {
			s.State = state
			if msg != "" {
				s.Message = msg
			}
		}
		if s.State == "" {
			s.State = "idle"
		}
	}
	return sessions, nil
}

// claudeRecord is what Claude Code keeps about a running session in ~/.claude/sessions.
type claudeRecord struct {
	SessionID       string `json:"sessionId"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	StatusUpdatedAt int64  `json:"statusUpdatedAt"`
}

func readClaudeRecord(pid int) claudeRecord {
	var rec claudeRecord
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		dir = filepath.Join(paths.Home(), ".claude")
	}
	if b, err := os.ReadFile(filepath.Join(dir, "sessions", strconv.Itoa(pid)+".json")); err == nil {
		_ = json.Unmarshal(b, &rec)
	}
	if !sessionID.MatchString(rec.SessionID) {
		rec.SessionID = ""
	}
	return rec
}

// sessionID is the shape of a Claude Code conversation ID (it ends up on a command line).
var sessionID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{7,79}$`)

type procTable struct {
	args map[int]string
	kids map[int][]int
}

// processTable reads every process's parent and command line once.
func processTable(ctx context.Context) procTable {
	t := procTable{args: map[int]string{}, kids: map[int][]int{}}
	out, err := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=,ppid=,args=").Output()
	if err != nil {
		return t
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		t.args[pid] = strings.Join(f[2:], " ")
		t.kids[ppid] = append(t.kids[ppid], pid)
	}
	return t
}

// agentUnder finds another coding agent (Codex, Grok, Mantis) among a shell's descendants
// and says which; "" when there is none.
func (t procTable) agentUnder(shell int) string {
	if shell <= 0 {
		return ""
	}
	var walk func(pid, depth int) string
	walk = func(pid, depth int) string {
		if a := agentOfArgs(t.args[pid]); a != "" && a != "claude" {
			return a
		}
		if depth > 8 {
			return ""
		}
		for _, k := range t.kids[pid] {
			if a := walk(k, depth+1); a != "" {
				return a
			}
		}
		return ""
	}
	return walk(shell, 0)
}

// claudeUnder finds Claude Code among a shell's descendants: run by name, or as the
// versioned binary its installer keeps under …/claude/versions/. Zero when it isn't there.
func (t procTable) claudeUnder(shell int) int {
	if shell <= 0 {
		return 0
	}
	var walk func(pid, depth int) int
	walk = func(pid, depth int) int {
		arg0, _, _ := strings.Cut(t.args[pid], " ")
		if filepath.Base(arg0) == "claude" || strings.Contains(arg0, "/claude/versions/") {
			return pid
		}
		if depth > 8 {
			return 0
		}
		for _, k := range t.kids[pid] {
			if p := walk(k, depth+1); p != 0 {
				return p
			}
		}
		return 0
	}
	return walk(shell, 0)
}
