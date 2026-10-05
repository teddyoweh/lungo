package engine

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"skybuild/internal/sshx"
)

// The coding agents a session can run. Claude Code is the one sky knows best (hooks, its
// record of each conversation, logins); the others are recognised the same way, from the
// program in the pane and what its screen says, and can be started and picked up again.

// Agent is a coding agent's command-line tool.
type Agent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Agents are the agents sky can start, in the order they are offered.
var Agents = []Agent{
	{ID: "claude", Name: "Claude"},
	{ID: "codex", Name: "Codex"},
	{ID: "grok", Name: "Grok Build"},
	{ID: "mantis", Name: "Mantis"},
}

// AgentName is an agent's name for messages ("Codex"); "" for no agent.
func AgentName(id string) string {
	for _, a := range Agents {
		if a.ID == id {
			return a.Name
		}
	}
	return ""
}

// agentOfCommand recognises an agent by the name of the program in a pane (tmux's
// pane_current_command, cut to 15 characters on macOS: "grok-macos-aarc").
func agentOfCommand(cmd string) string {
	switch {
	case isClaudeCommand(cmd):
		return "claude"
	case cmd == "codex" || strings.HasPrefix(cmd, "codex-"):
		return "codex"
	case cmd == "grok" || strings.HasPrefix(cmd, "grok-"):
		return "grok"
	case cmd == "mantis":
		return "mantis"
	}
	return ""
}

// agentOfArgs recognises an agent by a process's command line: the program, or for agents
// written in Python or JavaScript, the script it runs (mantis is `python3 …/bin/mantis`).
func agentOfArgs(args string) string {
	f := strings.Fields(args)
	if len(f) == 0 {
		return ""
	}
	if a := agentOfProgram(f[0]); a != "" {
		return a
	}
	// An interpreter: the agent is the script it runs (its first word that isn't a flag).
	base := strings.ToLower(filepath.Base(f[0]))
	if strings.HasPrefix(base, "python") || base == "node" || base == "bun" || base == "deno" || base == "ruby" {
		for _, a := range f[1:] {
			if !strings.HasPrefix(a, "-") {
				return agentOfProgram(a)
			}
		}
	}
	return ""
}

// agentOfProgram recognises an agent by the path of its program or script.
func agentOfProgram(p string) string {
	base := filepath.Base(p)
	switch {
	case base == "claude" || strings.Contains(p, "/claude/versions/"):
		return "claude"
	case base == "codex" || strings.HasPrefix(base, "codex-"):
		return "codex"
	case base == "grok" || strings.HasPrefix(base, "grok-"):
		return "grok"
	case base == "mantis":
		return "mantis"
	}
	return ""
}

// agentLine is the command that starts an agent in a session: picking up a conversation (an
// ID, or "continue" for the latest one in the folder), or with a first message.
func agentLine(agent, resume, prompt string) string {
	q := func(s string) string { return " " + sshx.Quote(s) }
	switch agent {
	case "codex":
		switch {
		case resume == "continue":
			return "codex resume --last"
		case resume != "":
			return "codex resume" + q(resume)
		case prompt != "":
			return "codex" + q(prompt)
		}
		return "codex"
	case "grok":
		switch {
		case resume == "continue":
			return "grok --continue"
		case resume != "":
			return "grok --resume" + q(resume)
		case prompt != "":
			return "grok" + q(prompt)
		}
		return "grok"
	case "mantis":
		switch {
		case resume == "continue":
			return "mantis --continue"
		case resume != "":
			return "mantis --resume" + q(resume)
		}
		return "mantis"
	}
	return ""
}

// agentScreenState reads from the bottom of an agent's screen whether it is working or
// waiting for you, as screenState does for Claude.
func agentScreenState(agent, screen string) (string, string) {
	lines := strings.Split(screen, "\n")
	tail := lines[max(0, len(lines)-14):]
	lower := strings.ToLower(strings.Join(tail, "\n"))
	name := AgentName(agent)
	question := func() string {
		for i := len(tail) - 1; i >= 0; i-- {
			t := strings.TrimSpace(strings.Trim(tail[i], "│ "))
			if q := strings.Index(t, "? "); q > 0 && !strings.HasSuffix(t, "for shortcuts") {
				t = t[:q+1] // the question, without the explanation after it on the same line
			}
			if strings.HasSuffix(t, "?") && !strings.HasPrefix(t, "?") {
				return t
			}
		}
		return name + " is waiting for your choice"
	}
	switch agent {
	case "codex":
		if strings.Contains(lower, "esc to interrupt") {
			return "working", ""
		}
		if strings.Contains(lower, "enter continue") || strings.Contains(lower, "enter to continue") || strings.Contains(lower, "enter to confirm") || strings.Contains(lower, "› 1.") || strings.Contains(lower, "allow command") {
			return "waiting", question()
		}
	case "grok":
		if strings.Contains(lower, "esc:cancel") || strings.Contains(lower, "esc to interrupt") {
			return "working", ""
		}
		if strings.Contains(lower, "allow") && (strings.Contains(lower, "deny") || strings.Contains(lower, "❯ 1.")) {
			return "waiting", question()
		}
	default: // Mantis and others draw their prompts the way Claude does
		if state, msg := screenState(screen); state != "" {
			if state == "waiting" && strings.HasPrefix(msg, "Claude") {
				msg = name + strings.TrimPrefix(msg, "Claude")
			}
			return state, msg
		}
	}
	return "", ""
}

// agentTitle is an agent's window title without what it adds to the task name: its own
// name or the folder ("Fix the login bug | api", "Fix the login bug - grok").
func agentTitle(agent, title, path string) string {
	folder := filepath.Base(path)
	for _, sep := range []string{" | ", " - ", " · ", " — "} {
		title = strings.TrimSuffix(title, sep+agent)
		title = strings.TrimSuffix(title, sep+AgentName(agent))
		if folder != "" && folder != "." && folder != "/" && folder != "~" {
			title = strings.TrimSuffix(title, sep+folder)
		}
	}
	return strings.TrimSpace(title)
}

// AgentVersion is an agent installed on a machine and the version it reports ("" when it
// doesn't say).
type AgentVersion struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

var versionNumber = regexp.MustCompile(`\d+\.\d+(\.\d+)?`)

// AgentVersions lists the agents installed on a machine with their versions, asking them all
// at once. Mantis has no --version; uv, which installs it, knows.
func (e *Engine) AgentVersions(ctx context.Context, machine string) ([]AgentVersion, error) {
	script := `export PATH="$HOME/.local/bin:$HOME/.bun/bin:$HOME/.grok/bin:/opt/homebrew/bin:/usr/local/bin:$PATH"
for c in claude codex grok mantis; do
  command -v "$c" >/dev/null 2>&1 || continue
  ( if [ "$c" = mantis ]; then v=$(uv tool list 2>/dev/null | sed -n 's/^mantis[a-z-]* v//p' | head -1); else v=$("$c" --version </dev/null 2>/dev/null | head -1); fi
    printf '%s\t%s\n' "$c" "$v" ) &
done
wait; true`
	out, err := e.shOn(ctx, machine, script)
	if err != nil {
		return nil, err
	}
	var list []AgentVersion
	for _, l := range strings.Split(out, "\n") {
		id, v, _ := strings.Cut(strings.TrimSpace(l), "\t")
		if AgentName(id) == "" {
			continue
		}
		list = append(list, AgentVersion{ID: id, Version: versionNumber.FindString(v)})
	}
	return list, nil
}

// AgentsOn lists the agents installed on a machine (LocalMachine: this computer).
func (e *Engine) AgentsOn(ctx context.Context, machine string) ([]string, error) {
	script := `for c in claude codex grok mantis; do command -v "$c" >/dev/null 2>&1 && echo "$c"; done; true`
	out, err := e.shOn(ctx, machine, `export PATH="$HOME/.local/bin:$HOME/.bun/bin:$HOME/.grok/bin:/opt/homebrew/bin:/usr/local/bin:$PATH"; `+script)
	if err != nil {
		return nil, err
	}
	var list []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); AgentName(l) != "" {
			list = append(list, l)
		}
	}
	return list, nil
}
