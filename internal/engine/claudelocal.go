package engine

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"skybuild/internal/claudeacct"
	"skybuild/internal/config"
	"skybuild/internal/paths"
	"skybuild/internal/proc"
	"skybuild/internal/sshx"
)

// Claude in the sessions on this computer.
//
// They sign in as the account the machines use, so a switch away from a limited account
// reaches them too. This computer's own Claude Code login is left alone:
//   - the active account's token is kept in a file only you can read (claude-token);
//   - claude-login.sh defines a `claude` that starts Claude with that token;
//   - sky's tmux server tells the shells in its panes where that script is (SKY_CLAUDE_LOGIN),
//     and one line in ~/.zshenv (or ~/.bashrc) loads it there and nowhere else.
//
// Claude in any other terminal, or started with its own CLAUDE_CONFIG_DIR, keeps its own
// login. A Claude running with another login is moved like one on a machine (see
// MoveToNewLogin). macOS doesn't show another process's environment, so the wrapper leaves a
// mark instead: it touches claude-login/<pid of the shell> as it starts Claude with the
// token. A Claude whose shell has no mark newer than the token started without it, or before
// the account changed.

// localLoginLine loads the claude wrapper in the shells of sky's tmux server.
const localLoginLine = `[ -n "$SKY_CLAUDE_LOGIN" ] && [ -r "$SKY_CLAUDE_LOGIN" ] && . "$SKY_CLAUDE_LOGIN"  # skybuild:claude-login v1`

func localTokenFile() string   { return filepath.Join(paths.Root(), "claude-token") }
func localLoginScript() string { return filepath.Join(paths.Root(), "claude-login.sh") }
func localLoginMarks() string  { return filepath.Join(paths.Root(), "claude-login") }

// localLoginScriptText is claude-login.sh. An empty token file means "this computer's own
// login" (see ClaudeFollowLogin): the shell is still marked, so sessions started on the pool's
// token before you chose that are moved to it. `function claude` rather than `claude()`: zsh
// refuses the latter when an alias named claude exists, as it may when the script is loaded
// into a shell that is already running. The mark is written before the token is read (a
// switch in between costs one needless move, never a missed one), with >| in case the shell
// has noclobber set.
func localLoginScriptText() string {
	tok := sshx.Quote(localTokenFile())
	mark := sshx.Quote(localLoginMarks()) + `/$$`
	return `# Written by sky. Claude in Lungo's sessions on this computer signs in as the Claude account
# the machines use; Claude anywhere else keeps this computer's own login.
export SKY_CLAUDE_LOGIN=` + sshx.Quote(localLoginScript()) + `
function claude { if [ -z "$CLAUDE_CONFIG_DIR" ] && [ -r ` + tok + ` ]; then printf . 2>/dev/null >| ` + mark + `; if [ -s ` + tok + ` ]; then CLAUDE_CODE_OAUTH_TOKEN="$(cat ` + tok + `)" command claude "$@"; else command claude "$@"; fi; else command claude "$@"; fi; }
`
}

// localLoginPrefix goes in front of a Claude command typed into a local session's shell, so
// one started before the wrapper existed gets it too: ". ~/.skybuild/claude-login.sh && ".
func localLoginPrefix() string {
	p := paths.Tilde(localLoginScript())
	if strings.HasPrefix(p, "~/") && !strings.ContainsAny(p[2:], " '\"\\$`!*?[]{}()<>|&;#~") {
		return ". " + p + " && "
	}
	return ". " + sshx.Quote(localLoginScript()) + " && "
}

// localLoginAt is when the login sessions on this computer use last changed (to another pool
// account, or to this computer's own login); zero when sky has never pointed them anywhere.
func localLoginAt() time.Time {
	st, err := os.Stat(localTokenFile())
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// onOldLogin reports whether a Claude started from one of these shells (its parent, the
// pane's) runs on another login than the one sessions here have used since loginAt.
func onOldLogin(loginAt time.Time, shells ...int) bool {
	for _, pid := range shells {
		if pid <= 0 {
			continue
		}
		if st, err := os.Stat(filepath.Join(localLoginMarks(), strconv.Itoa(pid))); err == nil && !st.ModTime().Before(loginAt) {
			return false
		}
	}
	return true
}

// pruneLoginMarks forgets the marks of shells that have ended.
func pruneLoginMarks(ctx context.Context) {
	entries, err := os.ReadDir(localLoginMarks())
	if err != nil || len(entries) == 0 {
		return
	}
	procs := proc.List(ctx)
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err == nil {
			if _, alive := procs.Rows[pid]; alive {
				continue
			}
		}
		_ = os.Remove(filepath.Join(localLoginMarks(), e.Name()))
	}
}

// syncLocalLogin points the sessions on this computer at the active account: its token in
// the file, the wrapper, and the line that loads it. When they follow this computer's own
// login the file is left empty (Claude there then switches with /login like anywhere else).
// With no account in the pool the file is removed.
func (e *Engine) syncLocalLogin(ctx context.Context) error {
	if LocalTmux() == "" {
		return nil // no sessions on this computer
	}
	token, follow := "", false
	if c, err := config.Load(); err == nil {
		if id := activeID(c); id != "" {
			token = claudeacct.Token(id)
		}
		follow = c.Settings.ClaudeFollowLogin() && token != ""
	}
	if follow {
		token = ""
	} else if token == "" {
		if err := os.Remove(localTokenFile()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := paths.Ensure(); err != nil {
		return err
	}
	if err := os.MkdirAll(localLoginMarks(), 0o700); err != nil {
		return err
	}
	pruneLoginMarks(ctx)
	if _, err := paths.WriteFile(localLoginScript(), []byte(localLoginScriptText()), 0o600); err != nil {
		return err
	}
	content := ""
	if token != "" {
		content = token + "\n"
	}
	if _, err := paths.WriteFile(localTokenFile(), []byte(content), 0o600); err != nil {
		return err
	}
	return ensureLocalLoginLine()
}

// ensureLocalLoginLine adds localLoginLine to the startup file of your shell (zsh: ~/.zshenv,
// read by every zsh; bash: ~/.bashrc), replacing an older version of it.
func ensureLocalLoginLine() error {
	file := ".zshenv"
	switch filepath.Base(os.Getenv("SHELL")) {
	case "bash":
		file = ".bashrc"
	case "fish", "nu", "elvish":
		return nil // not a shell that can load the script
	}
	path := filepath.Join(paths.Home(), file)
	cur, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	next, changed := withManagedLine(string(cur), localLoginLine, "skybuild:claude-login")
	if !changed {
		return nil
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	_, err = paths.WriteFile(path, []byte(next), mode)
	return err
}

// withManagedLine is text with line in it once (added at the end) and no older line carrying
// the same marker.
func withManagedLine(text, line, marker string) (string, bool) {
	var keep []string
	found := false
	for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		switch {
		case l == line && !found:
			found = true
			keep = append(keep, l)
		case strings.Contains(l, "# "+marker):
			// an older version of the line, or a second copy
		default:
			keep = append(keep, l)
		}
	}
	if !found {
		keep = append(keep, line)
	}
	next := strings.TrimLeft(strings.Join(keep, "\n"), "\n") + "\n"
	return next, next != text
}
