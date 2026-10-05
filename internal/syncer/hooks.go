package syncer

import (
	"strings"

	"skybuild/internal/bootstrap"
)

// SessionHook is installed on every machine as ~/.local/bin/sky-session-hook. Claude Code
// runs it from hooks; it records what Claude is doing in this tmux session so `sky sessions`
// and the desktop app can show "working" or "needs you" and send notifications.
const SessionHook = `#!/bin/sh
# skybuild: records Claude's state for this tmux session (written by sky; overwritten on sync).
state="$1"
input=$(cat 2>/dev/null)
[ -n "$TMUX" ] || exit 0
s=$(tmux display-message -p '#S' 2>/dev/null) || exit 0
d="$HOME/.skybuild/status"
mkdir -p "$d"
flat=$(printf '%s' "$input" | tr '\n' ' ')
msg=$(printf '%s' "$flat" | sed -n 's/.*"message"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -c 300)
sid=$(printf '%s' "$flat" | sed -n 's/.*"session_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -c 80)
printf '{"session":"%s","state":"%s","at":%s,"message":"%s","sid":"%s"}\n' "$s" "$state" "$(date +%s)" "$msg" "$sid" > "$d/$s.json.tmp" && mv "$d/$s.json.tmp" "$d/$s.json"
exit 0
`

const hookCmd = `"$HOME/.local/bin/sky-session-hook"`

var skyHooks = map[string]string{
	"UserPromptSubmit": "working",
	"PostToolUse":      "working",
	"Notification":     "waiting",
	"Stop":             "idle",
}

// addSessionHooks appends sky's hooks to a settings map, keeping the user's own hooks.
func addSessionHooks(settings map[string]any) {
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	for event, state := range skyHooks {
		groups, _ := hooks[event].([]any)
		var kept []any
		for _, g := range groups {
			if !strings.Contains(mustJSON(g), "sky-session-hook") {
				kept = append(kept, g)
			}
		}
		kept = append(kept, map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": hookCmd + " " + state}},
		})
		hooks[event] = kept
	}
	settings["hooks"] = hooks
}

// installHook writes the hook script on the machine if it changed.
func (x *run) installHook() error {
	have := strings.Fields(x.shOK(shaFn + "h .local/bin/sky-session-hook 2>/dev/null"))
	if len(have) > 0 && have[0] == hash([]byte(SessionHook)) {
		return nil
	}
	x.change("session hook installed")
	if x.opts.DryRun {
		return nil
	}
	_, err := x.shIn("mkdir -p .local/bin && cat > .local/bin/sky-session-hook && chmod 755 .local/bin/sky-session-hook", SessionHook)
	return err
}

// PeekCommand is the `peek` command sky puts on every machine (and this computer): it shows
// a file on the screen of whoever is looking at the session in the app. The file's path goes
// out through the session's terminal as an escape sequence (wrapped for tmux to pass on),
// written to the pane's terminal device so it works from a program whose output is captured
// (Claude's shell commands).
const PeekCommand = `#!/bin/sh
# peek: show a file on the screen of the person using this session (in Lungo).
# Installed by sky; rewritten on sync.
if [ $# -eq 0 ] || [ "$1" = "-h" ] || [ "$1" = "--help" ]; then
  echo "usage: peek <file>...   show files on the screen of the person using this session" >&2
  exit 2
fi
tty=""
[ -n "$TMUX_PANE" ] && tty=$(tmux display-message -p -t "$TMUX_PANE" '#{pane_tty}' 2>/dev/null)
if [ -z "$tty" ] || [ ! -w "$tty" ]; then
  echo "peek: not in a Lungo session (there is no tmux pane to send through)" >&2
  exit 1
fi
rc=0
for f in "$@"; do
  if [ ! -e "$f" ]; then echo "peek: no such file: $f" >&2; rc=1; continue; fi
  d=$(cd "$(dirname -- "$f")" 2>/dev/null && pwd -P) || { rc=1; continue; }
  p="$d/$(basename -- "$f")"
  [ -d "$f" ] && p=$(cd "$f" && pwd -P)
  b=$(printf '%s' "$p" | base64 | tr -d '\n')
  printf '\033Ptmux;\033\033]7338;%s\007\033\\' "$b" > "$tty"
  echo "Shown on the user's screen: $p"
done
exit $rc
`

// installPeek writes the peek command on the machine if it changed.
func (x *run) installPeek() error {
	have := strings.Fields(x.shOK(shaFn + "h .local/bin/peek 2>/dev/null"))
	if len(have) > 0 && have[0] == hash([]byte(PeekCommand)) {
		return nil
	}
	x.change("peek command installed")
	if x.opts.DryRun {
		return nil
	}
	_, err := x.shIn("mkdir -p .local/bin && cat > .local/bin/peek && chmod 755 .local/bin/peek", PeekCommand)
	return err
}

const tmuxSourceLine = "source-file -q ~/.skybuild/tmux.conf  # skybuild"

// syncTmux keeps sky's tmux settings current on the machine (scroll feel, titles, extended
// keys) and reloads a running tmux so open sessions pick them up.
func (x *run) syncTmux() error {
	if strings.TrimSpace(x.shOK("command -v tmux")) == "" {
		return nil
	}
	changed := false
	have := strings.Fields(x.shOK(shaFn + "h .skybuild/tmux.conf 2>/dev/null"))
	if len(have) == 0 || have[0] != hash([]byte(bootstrap.TmuxManaged)) {
		x.change("tmux settings updated")
		if err := x.writeRemote(".skybuild/tmux.conf", bootstrap.TmuxManaged); err != nil {
			return err
		}
		changed = true
	}
	if !strings.Contains(x.shOK("cat .tmux.conf 2>/dev/null"), "~/.skybuild/tmux.conf") {
		x.change("~/.tmux.conf: loads sky's tmux settings")
		if !x.opts.DryRun {
			if _, err := x.shIn("cat >> .tmux.conf", tmuxSourceLine+"\n"); err != nil {
				return err
			}
		}
		changed = true
	}
	if changed && !x.opts.DryRun {
		x.shOK("tmux source-file ~/.tmux.conf 2>/dev/null")
	}
	return nil
}
