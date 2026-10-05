package syncer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"skybuild/internal/config"
	"skybuild/internal/model"
	"skybuild/internal/sshx"
)

// Stop when idle. The check has to work while this computer is asleep, so it lives on the
// machine: a script run by a systemd timer every two minutes. It decides whether anything is
// going on; if so it touches a stamp, and once the stamp is older than the limit it shuts the
// machine down. On Google Cloud and AWS a machine that shuts itself down stops being billed
// for compute, exactly as if it had been stopped from outside.

// IdleWatchdog is that script. It is installed as /usr/local/sbin/sky-idle-watch and runs as
// root. "Active" means any of:
//   - someone has a tmux session open (a client is attached), or had one since the last check
//   - Claude is working: its screen says "esc to interrupt", or its hook reported a change
//     since the last check, or reported "working" within the last ten minutes. (The hook's
//     state alone is not enough: a turn interrupted with Esc leaves it at "working" for good.)
//   - someone is logged in over ssh with a terminal (sky's own background commands have none)
//   - the 15-minute load is above 5% of the cores (at least 0.15, at most 0.5, so one busy
//     core always counts however large the machine is)
//
// A boot counts as activity too, so a machine that was just started always gets a full limit.
// Every run writes one line to /var/log/skybuild-idle.log and its decision to
// /var/lib/skybuild/idle/state, which the health check reads.
const IdleWatchdog = idleHead + idleFuncs + idleMain

const idleHead = `#!/bin/sh
# skybuild: stops this machine once nothing has happened on it for LIMIT_MIN minutes.
# Written by sky and rewritten on sync; the setting is in /etc/skybuild/idle.conf.
# Run by sky-idle.timer every two minutes. "sky-idle-watch --dry-run" only logs.
`

// idleFuncs are the two decisions the watchdog makes, apart from the rest of the script so
// the tests can run exactly this text.
const idleFuncs = `
# load_busy <15-minute load> <cores>: is the load above 5% of the cores? The bar is never
# below 0.15 (background noise) nor above 0.5 (one busy core must always count).
load_busy() {
  awk -v l="$1" -v c="$2" 'BEGIN { t = c * 0.05; if (t < 0.15) t = 0.15; if (t > 0.5) t = 0.5; exit !(l > t) }'
}

# decide <now> <stamp> <boot> <limit minutes> <dry run 0|1> <reason>: what to do now.
# The stamp is when something last happened; a boot later than that counts as well, so a
# machine that was just started always gets a full limit. Times are in seconds.
# Prints "<stop 0|1> <idle minutes> <idle since> <log line>".
decide() {
  if [ -n "$6" ]; then echo "0 0 $1 active: $6"; return; fi
  d_since=$2
  [ "$3" -gt "$d_since" ] && d_since=$3
  d_idle=$((($1 - d_since) / 60))
  if [ "$d_idle" -lt "$4" ]; then echo "0 $d_idle $d_since idle ${d_idle}m of ${4}m"
  elif [ "$5" = 1 ]; then echo "0 $d_idle $d_since idle ${d_idle}m, limit ${4}m: would stop (dry run)"
  else echo "1 $d_idle $d_since idle ${d_idle}m, limit ${4}m: stopping"
  fi
}
`

const idleMain = `
CONF=${SKY_IDLE_CONF:-/etc/skybuild/idle.conf}
STATE=${SKY_IDLE_STATE:-/var/lib/skybuild/idle}
LOG=${SKY_IDLE_LOG:-/var/log/skybuild-idle.log}
LIMIT_MIN=0
DRY_RUN=0
[ -r "$CONF" ] && . "$CONF"
[ "${1:-}" = "--dry-run" ] && DRY_RUN=1
case "$LIMIT_MIN" in ''|*[!0-9]*) LIMIT_MIN=0 ;; esac
[ "$LIMIT_MIN" -gt 0 ] || exit 0
mkdir -p "$STATE" || exit 1
now=$(date +%s)
last=$(stat -c %Y "$STATE/lastrun" 2>/dev/null || echo "$now")
TAB=$(printf '\t')
reason=""

# tm runs tmux against one server, as the user who owns it. It only asks questions: none of
# these commands starts a server or changes a session.
tm() {
  if [ "$(id -u)" = 0 ]; then runuser -u "$owner" -- tmux -S "$sock" "$@" 2>/dev/null
  else tmux -S "$sock" "$@" 2>/dev/null; fi
}

for sock in /tmp/tmux-*/*; do
  [ -S "$sock" ] || continue
  owner=$(stat -c %U "$sock" 2>/dev/null) || continue
  home=$(getent passwd "$owner" | cut -d: -f6)

  if [ -n "$(tm list-clients -F x)" ]; then reason="tmux client attached"; break; fi
  for t in $(tm list-sessions -F '#{session_last_attached}'); do
    case "$t" in ''|*[!0-9]*) continue ;; esac
    if [ "$t" -gt "$last" ]; then reason="a tmux session was open a moment ago"; break; fi
  done
  [ -n "$reason" ] && break

  busy=$(tm list-panes -a -F "#{pane_id}${TAB}#{pane_current_command}${TAB}#{session_name}" | while IFS="$TAB" read -r id cmd s; do
    case "$cmd" in (*claude*|node|[0-9]*.[0-9]*.[0-9]*) ;; (*) continue ;; esac
    if tm capture-pane -p -t "$id" | grep -v '^[[:space:]]*$' | tail -n 14 | grep -qi 'esc to interrupt'; then
      echo "$s"; break
    fi
  done)
  if [ -n "$busy" ]; then reason="Claude is working in $busy"; break; fi

  for f in "$home"/.skybuild/status/*.json; do
    [ -f "$f" ] || continue
    m=$(stat -c %Y "$f" 2>/dev/null) || continue
    s=$(basename "$f" .json)
    if [ "$m" -gt "$last" ]; then reason="Claude was active in $s a moment ago"; break; fi
    if [ $((now - m)) -lt 600 ] && grep -q '"state":"working"' "$f" && tm has-session -t "=$s"; then
      reason="Claude is working in $s"; break
    fi
  done
  [ -n "$reason" ] && break
done

if [ -z "$reason" ] && ps -eo args= | grep -Eq '^sshd(-session)?: [^ ]+@pts/'; then
  reason="ssh login"
fi

if [ -z "$reason" ]; then
  read -r l1 l5 l15 rest < /proc/loadavg
  if load_busy "$l15" "$(nproc 2>/dev/null || echo 1)"; then reason="load $l15 over 15 minutes"; fi
fi

stamp=$(stat -c %Y "$STATE/stamp" 2>/dev/null || echo 0)
boot=$((now - $(cut -d. -f1 /proc/uptime)))
out=$(decide "$now" "$stamp" "$boot" "$LIMIT_MIN" "$DRY_RUN" "$reason")
stop=${out%% *}; out=${out#* }
idle=${out%% *}; out=${out#* }
since=${out%% *}; line=${out#* }
active=0
if [ -n "$reason" ]; then active=1; touch "$STATE/stamp"; fi

printf '%s %s\n' "$(date -u +%FT%TZ)" "$line" >> "$LOG"
if [ "$(wc -l < "$LOG")" -gt 2000 ]; then tail -n 1000 "$LOG" > "$LOG.tmp" && mv "$LOG.tmp" "$LOG"; fi
printf 'at=%s\nactive=%s\nsince=%s\nlimit=%s\ndry=%s\nreason=%s\n' "$now" "$active" "$since" "$LIMIT_MIN" "$DRY_RUN" "$reason" > "$STATE/state.tmp" \
  && mv "$STATE/state.tmp" "$STATE/state"
touch "$STATE/lastrun"

if [ "$stop" = 1 ] && [ "$DRY_RUN" != 1 ]; then
  printf 'at=%s\nidle=%s\nlimit=%s\n' "$now" "$idle" "$LIMIT_MIN" > "$STATE/last-stop"
  sync
  systemctl poweroff || shutdown -h now
fi
exit 0
`

const idleService = `[Unit]
Description=skybuild: stop this machine when it has been idle

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/sky-idle-watch
`

// The first check comes a minute after the timer starts (at boot, or when the setting is
// turned on), the rest two minutes apart.
const idleTimer = `[Unit]
Description=skybuild: check every two minutes whether this machine is idle

[Timer]
OnActiveSec=1min
OnUnitActiveSec=2min
AccuracySec=10s

[Install]
WantedBy=timers.target
`

// Where the watchdog keeps its files on a machine.
const (
	IdleStatePath = "/var/lib/skybuild/idle/state"
	IdleConfPath  = "/etc/skybuild/idle.conf"
	IdleLogPath   = "/var/log/skybuild-idle.log"
)

// IdleMinMinutes is the shortest limit: the watchdog looks every two minutes, and anything
// shorter would stop a machine between two commands.
const IdleMinMinutes = 5

// IdleOffered reports whether stop-when-idle can be turned on for a machine, and why not.
//
// It is for cloud machines sky created. A machine you added yourself is never stopped by sky.
// On Azure it is not offered: a VM that shuts itself down there stays allocated and keeps
// being billed, and deallocating it has to be done from outside, by a computer that is awake,
// which is exactly when this is not needed.
func IdleOffered(m *model.Machine) (bool, string) {
	switch {
	case !m.IsCloud():
		return false, "Only for machines sky created: sky never stops a machine you added yourself."
	case m.Provider == model.ProviderAzure:
		return false, "Not on Azure: a VM that shuts itself down there keeps being billed until it is deallocated from outside."
	case m.OS != "" && m.OS != "linux":
		return false, "Only for Linux machines."
	}
	return true, ""
}

func idleConf(s config.IdleStop) string {
	dry := 0
	if s.DryRun {
		dry = 1
	}
	return fmt.Sprintf("# Written by sky (`sky idle <machine> <off|1h|2h|…>`).\nLIMIT_MIN=%d\nDRY_RUN=%d\n", s.Minutes, dry)
}

// idleDigest identifies everything installed for a setting, so a change to the script, the
// units or the setting reinstalls and nothing else does.
func idleDigest(s config.IdleStop) string {
	return hash([]byte(IdleWatchdog + "\x00" + idleService + "\x00" + idleTimer + "\x00" + idleConf(s)))[:16]
}

// idleInstall is the script that puts the watchdog in place; it runs as root. The stamp is
// touched so the limit counts from now: a machine that sat idle all day doesn't stop the
// moment the setting is turned on.
func idleInstall(s config.IdleStop) string {
	file := func(path, mode, content string) string {
		return "cat > " + path + " <<'SKY_IDLE_EOF'\n" + content + "SKY_IDLE_EOF\nchmod " + mode + " " + path + "\n"
	}
	return "set -e\n" +
		"install -d -m 755 /etc/skybuild /var/lib/skybuild/idle\n" +
		file("/usr/local/sbin/sky-idle-watch", "755", IdleWatchdog) +
		file(IdleConfPath, "644", idleConf(s)) +
		file("/etc/systemd/system/sky-idle.service", "644", idleService) +
		file("/etc/systemd/system/sky-idle.timer", "644", idleTimer) +
		"systemctl daemon-reload\n" +
		"systemctl enable sky-idle.timer >/dev/null 2>&1\n" +
		"systemctl restart sky-idle.timer\n" +
		"touch /var/lib/skybuild/idle/stamp\n" +
		"rm -f /var/lib/skybuild/idle/state\n" +
		"echo " + idleDigest(s) + " > /var/lib/skybuild/idle/installed\n"
}

// idleRemove takes the watchdog off a machine. The log stays: it says why the machine
// stopped the last time.
const idleRemove = `systemctl disable --now sky-idle.timer >/dev/null 2>&1
rm -f /etc/systemd/system/sky-idle.timer /etc/systemd/system/sky-idle.service /usr/local/sbin/sky-idle-watch /etc/skybuild/idle.conf
rm -f /var/lib/skybuild/idle/installed /var/lib/skybuild/idle/state /var/lib/skybuild/idle/stamp /var/lib/skybuild/idle/lastrun
systemctl daemon-reload
`

// idleProbe asks what is installed without needing root: the digest, whether the timer's
// unit is there, and whether the timer is running.
const idleProbe = `cat /var/lib/skybuild/idle/installed 2>/dev/null || echo none; [ -e /etc/systemd/system/sky-idle.timer ] && echo present || echo absent; systemctl is-active sky-idle.timer 2>/dev/null; true`

// ApplyIdle makes the watchdog on a machine match the setting: installed and current when it
// is on, gone when it is off. It returns what it changed ("" when nothing needed doing). With
// dryRun it only says what it would change.
func ApplyIdle(ctx context.Context, t sshx.Target, s config.IdleStop, dryRun bool) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	out, err := sshx.Run(ctx, t, idleProbe)
	if err != nil {
		return "", err
	}
	f := strings.Fields(out)
	for len(f) < 3 {
		f = append(f, "")
	}
	installed, present, active := f[0], f[1] == "present", f[2] == "active"
	if s.Minutes <= 0 {
		if !present {
			return "", nil
		}
		if dryRun {
			return "stop when idle turned off", nil
		}
		if _, err := sshx.RunInput(ctx, t, "sudo -n sh -s", strings.NewReader(idleRemove)); err != nil {
			return "", sudoErr(err)
		}
		return "stop when idle turned off", nil
	}
	if installed == idleDigest(s) && present && active {
		return "", nil
	}
	what := "stop when idle: after " + IdleLimit(s.Minutes) + " with nothing going on"
	if s.DryRun {
		what += " (dry run: it only logs)"
	}
	if dryRun {
		return what, nil
	}
	if _, err := sshx.RunInput(ctx, t, "sudo -n sh -s", strings.NewReader(idleInstall(s))); err != nil {
		return "", sudoErr(err)
	}
	return what, nil
}

func sudoErr(err error) error {
	if strings.Contains(err.Error(), "a password is required") {
		return errors.New("stop when idle needs sudo without a password on the machine")
	}
	return err
}

// IdleLimit writes a limit the way people say it: "2h", "45m", "1h 30m".
func IdleLimit(minutes int) string {
	h, m := minutes/60, minutes%60
	switch {
	case h == 0:
		return fmt.Sprintf("%dm", m)
	case m == 0:
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}

// syncIdle keeps the watchdog in line with the machine's stop-when-idle setting. It is not an
// item to toggle: the setting itself is the switch.
func (x *run) syncIdle() error {
	if x.remoteOS != "linux" {
		return nil
	}
	var want config.IdleStop
	if ok, _ := IdleOffered(x.m); ok {
		c, err := config.Load()
		if err != nil {
			return err
		}
		want = c.IdleFor(x.m.Name)
	} else if !x.m.IsCloud() {
		return nil // never installed there, so there is nothing to take away either
	}
	what, err := ApplyIdle(x.ctx, x.t, want, x.opts.DryRun)
	if err != nil {
		return err
	}
	if what != "" {
		x.change("%s", what)
	}
	return nil
}
