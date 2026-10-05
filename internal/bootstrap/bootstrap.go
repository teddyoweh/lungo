// Package bootstrap renders the script a new machine runs on first boot.
package bootstrap

import (
	_ "embed"
	"strings"
	"text/template"
)

//go:embed setup.sh.tmpl
var script string

// Version is stamped into the script and the setup log.
var Version = "dev"

// Options fill the script.
type Options struct {
	Name        string
	User        string
	PublicKey   string
	DataDevices string // space-separated device paths to try, "auto" to find it by size, "" for none
	DataGB      int
	Docker      bool
	Tailscale   bool
	AutoTmux    bool
	Existing    bool // a machine the user already had: keep its hostname, shell and dotfiles
	Version     string
	TmuxConf    string // filled in by Render
}

// Device hints per provider: where the data volume shows up inside the VM.
const (
	DevicesGCP   = "/dev/disk/by-id/google-skydata"
	DevicesAWS   = "auto"
	DevicesAzure = "/dev/disk/azure/data/by-lun/0 /dev/disk/azure/scsi1/lun0 auto"
)

var tmpl = template.Must(template.New("setup").Funcs(template.FuncMap{
	"q": func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" },
}).Parse(script))

// Render returns the script for these options.
func Render(o Options) string {
	if o.Version == "" {
		o.Version = Version
	}
	o.TmuxConf = TmuxConf
	var b strings.Builder
	if err := tmpl.Execute(&b, o); err != nil {
		panic(err) // the template is embedded; a failure here is a programming error
	}
	return b.String()
}

// LogPath is where the script writes its log on the machine.
const LogPath = "/var/log/skybuild-setup.log"

// ReadyPath exists once setup finished.
const ReadyPath = "/var/lib/skybuild/ready"

// TmuxConf is the tmux setup sky gives machines that have none: mouse, long history, true
// colour, extended keys (shift-enter in Claude), and the pane's title passed to the terminal.
const TmuxConf = `# skybuild defaults
if-shell "test -x /usr/bin/zsh" "set -g default-shell /usr/bin/zsh"
set -g mouse on
set -g history-limit 200000
set -g escape-time 10
set -g default-terminal "tmux-256color"
set -as terminal-features ",*:RGB"
set -s extended-keys on
set -as terminal-features "xterm*:extkeys"
set -g set-clipboard on
set -g set-titles on
set -g set-titles-string "#{?#{||:#{==:#{pane_title},#{host}},#{==:#{pane_title},#{host_short}}},,#{pane_title}}"
set -g allow-passthrough on
set -g base-index 1
setw -g pane-base-index 1
set -g status-style "bg=default,fg=colour245"
set -g status-left "#[fg=colour39,bold] #h #[default]"
set -g status-left-length 30
set -g status-right "#[fg=colour245]#S  %H:%M "
setw -g window-status-current-style "fg=colour255,bold"
source-file -q ~/.skybuild/tmux.conf
`

// TmuxManaged is the part of the tmux setup sky owns on every machine. Sync writes it to
// ~/.skybuild/tmux.conf (sourced from ~/.tmux.conf) and reloads tmux, so improvements reach
// machines that already exist.
const TmuxManaged = `# Managed by skybuild: rewritten on sync. Your own settings go in ~/.tmux.conf.
set -g mouse on
set -g history-limit 200000
set -g escape-time 10
set -s extended-keys on
set -as terminal-features "xterm*:extkeys"
set -as terminal-features ",*:RGB"
set -g set-clipboard on
set -g allow-passthrough on
# No status line: the app shows the machine, the session and its state, and in a plain
# terminal the row is better spent on the session. ("set -g status on" in ~/.tmux.conf after
# the line that loads this file brings it back.)
set -g status off
set -g set-titles on
# The pane's title, or nothing when it is only the hostname (so tabs can show the folder).
set -g set-titles-string "#{?#{||:#{==:#{pane_title},#{host}},#{==:#{pane_title},#{host_short}}},,#{pane_title}}"
# Scrolling: one line per wheel step (tmux's default of five makes a trackpad feel jumpy).
# The app sends one step per line of finger travel, so this tracks the gesture 1:1.
bind -T copy-mode WheelUpPane select-pane \; send-keys -X -N 1 scroll-up
bind -T copy-mode WheelDownPane select-pane \; send-keys -X -N 1 scroll-down
bind -T copy-mode-vi WheelUpPane select-pane \; send-keys -X -N 1 scroll-up
bind -T copy-mode-vi WheelDownPane select-pane \; send-keys -X -N 1 scroll-down
# Scrolling back puts a pane in copy mode, where keys move around the history instead of
# reaching the program. The app sends this key (one no keyboard has) before typing into a
# pane it scrolled back in, so the typing goes to Claude or the shell, as in any terminal.
# @sky-keys tells the app this tmux knows the key.
set -s user-keys[90] "\e[9001~"
bind -T root User90 if -F "#{pane_in_mode}" "send-keys -X cancel"
bind -T copy-mode User90 send-keys -X cancel
bind -T copy-mode-vi User90 send-keys -X cancel
set -g @sky-keys 1
`
