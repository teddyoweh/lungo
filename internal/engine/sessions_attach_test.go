package engine

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestAttachScript(t *testing.T) {
	// Attaching to a session that exists changes nothing about it; one that doesn't is made in the home folder.
	if got := attachScript("main", AttachOptions{}); got != `s='main'; cd "$HOME"; if ! tmux has-session -t "=$s" 2>/dev/null; then tmux new-session -d -s "$s" -c "$PWD" && tmux set-option -t "=$s:" status off >/dev/null 2>&1; fi; exec tmux attach-session -t "=$s"` {
		t.Errorf("plain attach: %q", got)
	}
	shell := attachScript("shell-ab12", AttachOptions{Dir: "~/code/my api"})
	for _, want := range []string{`s='shell-ab12'`, `cd "$HOME"/'code/my api' 2>/dev/null || { cd "$HOME"; gone='~/code/my api'; }`, `tmux has-session -t "=$s"`, `tmux new-session -d -s "$s" -c "$PWD" && tmux set-option -t "=$s:" status off >/dev/null 2>&1; fi`, `exec tmux attach-session -t "=$s"`} {
		if !strings.Contains(shell, want) {
			t.Errorf("shell script misses %q:\n%s", want, shell)
		}
	}
	if strings.Contains(shell, "claude") {
		t.Errorf("shell script starts claude:\n%s", shell)
	}
	claude := attachScript("claude-api", AttachOptions{Dir: "/srv/api", Claude: true})
	for _, want := range []string{`cd '/srv/api'`, `line='claude'; tmux new-session`, `if [ -n "$gone" ]; then tmux send-keys -t "=$s:" "# skybuild: $gone is not there any more, so nothing was started" Enter; else tmux send-keys -t "=$s:" "$line" Enter; fi`, "trust this folder", `& fi; exec tmux attach-session`} {
		if !strings.Contains(claude, want) {
			t.Errorf("claude script misses %q:\n%s", want, claude)
		}
	}
	// A lost Claude session comes back with its conversation: by ID when its transcript is on
	// the machine, else the latest one in the folder. An ID that isn't one is never typed.
	resume := attachScript("claude-api", AttachOptions{Dir: "/srv/api", Resume: "0b9d7c1e-5a42-4c8e-9a51-2f6f0c7d1e11"})
	for _, want := range []string{`/.claude/projects/*/'0b9d7c1e-5a42-4c8e-9a51-2f6f0c7d1e11.jsonl'`, `line='claude --resume 0b9d7c1e-5a42-4c8e-9a51-2f6f0c7d1e11'`, `else line='claude'; fi;`, "trust this folder"} {
		if !strings.Contains(resume, want) {
			t.Errorf("resume script misses %q:\n%s", want, resume)
		}
	}
	latest := attachScript("claude-api", AttachOptions{Dir: "/srv/api", Resume: "continue"})
	if !strings.Contains(latest, `line='claude --continue'; tmux new-session`) || strings.Contains(latest, "--resume") {
		t.Errorf("continue script:\n%s", latest)
	}
	if bad := attachScript("x", AttachOptions{Resume: "$(rm -rf ~); x"}); strings.Contains(bad, "rm -rf") || !strings.Contains(bad, `line='claude --continue'`) {
		t.Errorf("an odd resume value must fall back to --continue:\n%s", bad)
	}
	// Claude comes back the way it was started: its permission flags, and nothing else.
	if got := ClaudeFlags("/home/u/.local/share/claude/versions/2.1.9 --resume abc --dangerously-skip-permissions --model x; rm -rf / --permission-mode plan --permission-mode $(id)"); got != "--dangerously-skip-permissions --permission-mode plan" {
		t.Errorf("ClaudeFlags: %q", got)
	}
	flagged := attachScript("claude-api", AttachOptions{Dir: "/srv/api", Resume: "0b9d7c1e-5a42-4c8e-9a51-2f6f0c7d1e11", Flags: "--dangerously-skip-permissions ; touch /tmp/x $(id)"})
	if !strings.Contains(flagged, `line='claude --resume 0b9d7c1e-5a42-4c8e-9a51-2f6f0c7d1e11 --dangerously-skip-permissions'`) || !strings.Contains(flagged, `else line='claude --dangerously-skip-permissions'; fi;`) || strings.Contains(flagged, "touch") {
		t.Errorf("flags: %s", flagged)
	}
	// A session last seen after tmux started was ended on purpose: it is not made again.
	seen := attachScript("work", AttachOptions{Dir: "/srv", Seen: 1791000120})
	if !strings.Contains(seen, `then st=$(tmux display-message -p '#{start_time}' 2>/dev/null); if [ -n "$st" ] && [ "$st" -lt 1791000000 ]; then echo '[gone]'; exit 0; fi; tmux new-session -d`) {
		t.Errorf("seen: %s", seen)
	}
	// A recipe: Claude starts with a first message (through a file, so quotes and newlines are
	// safe), a shell with a command typed into it.
	first := attachScript("claude-api", AttachOptions{Dir: "/srv/api", Claude: true, Prompt: "fix the 'auth' test\nthen $(rm -rf ~)"})
	for _, want := range []string{`mkdir -p "$HOME"/.skybuild/prompts && printf '%s' 'fix the '\''auth'\'' test`, `> "$HOME"/.skybuild/prompts/'claude-api.txt'`, `line='claude "$(cat ~/.skybuild/prompts/claude-api.txt)"';`} {
		if !strings.Contains(first, want) {
			t.Errorf("prompt script misses %q:\n%s", want, first)
		}
	}
	if again := attachScript("claude-api", AttachOptions{Dir: "/srv/api", Resume: "continue", Prompt: "hello"}); strings.Contains(again, "prompts") {
		t.Errorf("a resumed session takes no first message:\n%s", again)
	}
	cmd := attachScript("shell-ab12", AttachOptions{Dir: "/srv/api", Run: "npm run dev\nrm -rf /"})
	if !strings.Contains(cmd, `line='npm run dev'; tmux new-session`) || !strings.Contains(cmd, `else tmux send-keys -t "=$s:" "$line" Enter; fi; fi; exec tmux attach`) || strings.Contains(cmd, "rm -rf") {
		t.Errorf("run script:\n%s", cmd)
	}
	// On this computer tmux is a shell function, which exec would bypass.
	if local := attachScriptWith("tmux", "shell-ab12", AttachOptions{Dir: "~"}); strings.Contains(local, "exec ") || !strings.HasSuffix(local, `; tmux attach-session -t "=$s"`) {
		t.Errorf("local script: %s", local)
	}
	if home := attachScript("x", AttachOptions{Dir: "~", Claude: true}); !strings.Contains(home, `; cd "$HOME"; if`) {
		t.Errorf("home: %s", home)
	}
	// Every variant must be a script the remote shell accepts.
	if runtime.GOOS != "windows" {
		for _, script := range []string{shell, claude, resume, latest, flagged, seen, first, cmd, attachScript("it's", AttachOptions{Dir: "~/a b/c'd", Claude: true})} {
			if out, err := exec.Command("sh", "-n", "-c", script).CombinedOutput(); err != nil {
				t.Errorf("sh rejects the script: %v\n%s\n%s", err, out, script)
			}
		}
	}
}

// Another agent is started by its own command, picking a conversation up when asked.
func TestAttachAnotherAgent(t *testing.T) {
	cases := []struct {
		o    AttachOptions
		want string
	}{
		{AttachOptions{Agent: "codex"}, `line='codex'`},
		{AttachOptions{Agent: "codex", Resume: "continue"}, `line='codex resume --last'`},
		{AttachOptions{Agent: "grok", Resume: "0198a3c4-1111-7222-8333-444455556666"}, `grok --resume`},
		{AttachOptions{Agent: "mantis", Resume: "continue"}, `line='mantis --continue'`},
	}
	for _, c := range cases {
		got := attachScriptWith("tmux", "agent-x", c.o)
		if !strings.Contains(got, c.want) {
			t.Errorf("%+v: want %q in\n%s", c.o, c.want, got)
		}
		if strings.Contains(got, "trust this folder") || strings.Contains(got, "line='claude") {
			t.Errorf("%+v: started Claude:\n%s", c.o, got)
		}
	}
}

// The other agents are recognised by the program in the pane or its command line, and
// their state is read from their screens.
func TestOtherAgents(t *testing.T) {
	for cmd, want := range map[string]string{"codex": "codex", "grok-macos-aarc": "grok", "2.1.289": "claude", "zsh": "", "python3.14": ""} {
		if got := agentOfCommand(cmd); got != want {
			t.Errorf("agentOfCommand(%q) = %q, want %q", cmd, got, want)
		}
	}
	for args, want := range map[string]string{
		"/opt/homebrew/Cellar/python@3.14/3.14.7/Frameworks/Python.framework/Versions/3.14/Resources/Python.app/Contents/MacOS/Python /Users/t/.local/bin/mantis": "mantis",
		"/opt/homebrew/bin/codex":                                "codex",
		"/Users/t/.grok/bin/grok-macos-aarch64 --always-approve": "grok",
		"node /usr/lib/x.js mantis":                              "",
	} {
		if got := agentOfArgs(args); got != want {
			t.Errorf("agentOfArgs(%q) = %q, want %q", args, got, want)
		}
	}
	if st, _ := agentScreenState("mantis", "❯ fix it\n✳ Levitating… (1s · esc to interrupt)\n❯"); st != "working" {
		t.Errorf("mantis working: %q", st)
	}
	if st, _ := agentScreenState("grok", "│ ❯\n  Shift+Tab:mode  │  Esc:cancel  │  Ctrl+.:shortcuts"); st != "working" {
		t.Errorf("grok working: %q", st)
	}
	if st, _ := agentScreenState("grok", "│ ❯\n  Shift+Tab:mode  │  Ctrl+.:shortcuts"); st != "" {
		t.Errorf("grok idle read as %q", st)
	}
	if _, msg := agentScreenState("codex", "  Trust this folder? Codex can read, edit, and run files here.\n› 1. Trust and continue\n  enter continue · esc back"); msg != "Trust this folder?" {
		t.Errorf("codex trust question: %q", msg)
	}
	if st, msg := agentScreenState("codex", "› 1. Use session directory\n  2. Use current directory\n  enter continue · esc use session"); st != "waiting" || msg == "" {
		t.Errorf("codex choice: %q %q", st, msg)
	}
	out := strings.Join([]string{
		"codex-a\t1791080000\t1791080100\t0\t1\t/home/t\tcodex\tdemo\tdemo",
		"py-b\t1791080000\t1791080100\t0\t1\t/home/t\tpython3.14\tdemo\tdemo",
		"node-c\t1791080000\t1791080100\t0\t1\t/tmp\tnode\tdemo\tdemo",
		"@@sky-status@@",
		"@@sky-pane@@codex-a\t4242 4200 01:00 /usr/local/bin/codex\t",
		"Working (3s • esc to interrupt)",
		"@@sky-pane@@py-b\t4343 4300 02:00 /usr/bin/python3 /home/t/.local/bin/mantis\t",
		"❯",
		"@@sky-pane@@node-c\t4444 4400 03:00 node /opt/homebrew/bin/codex\t ",
		"  Press enter to continue",
	}, "\n")
	by := map[string]Session{}
	for _, s := range parseSessions("demo", out) {
		by[s.Name] = s
	}
	if s := by["codex-a"]; s.Agent != "codex" || s.State != "working" || s.Claude {
		t.Errorf("codex session: %+v", s)
	}
	if s := by["py-b"]; s.Agent != "mantis" || s.State != "idle" || s.Command != "mantis" {
		t.Errorf("mantis session: %+v", s)
	}
	if s := by["node-c"]; s.Agent != "codex" || s.Claude {
		t.Errorf("codex under node read as: %+v", s)
	}
}

// An agent's own name as the window title says nothing; a task name does.
func TestAgentTitles(t *testing.T) {
	for raw, want := range map[string]string{"grok": "", "mantis · agenttest": "", "Codex": "", "✳ Fix the login bug": "Fix the login bug"} {
		if got := cleanTitle(raw, "Mac"); got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", raw, got, want)
		}
	}
	for _, c := range [][4]string{
		{"codex", "Reply with pineapple | agenttest", "/tmp/agenttest", "Reply with pineapple"},
		{"grok", "Exact pineapple word reply request - grok", "/tmp/agenttest", "Exact pineapple word reply request"},
		{"mantis", "Fix a - b bug", "/tmp/api", "Fix a - b bug"},
	} {
		if got := agentTitle(c[0], c[1], c[2]); got != c[3] {
			t.Errorf("agentTitle(%q, %q) = %q, want %q", c[0], c[1], got, c[3])
		}
	}
}
