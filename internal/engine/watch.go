package engine

import (
	"bufio"
	"context"
	"os/exec"
	"strings"
	"time"
)

// watchSession is the hidden session the watcher keeps its tmux client on. It is never
// listed (see LocalSessions).
const watchSession = "_sky"

// watchFormat is what the watcher asks tmux to report when it changes: every session, and
// each pane's title (Claude's task and state), program and folder. tmux looks once a second
// and says so only when something changed.
const watchFormat = "#{S:#{session_name}#{W:#{P:|#{pane_title}|#{pane_current_command}|#{pane_current_path}}}}"

// WatchLocal calls changed as soon as something in this computer's sessions changes: one
// made or ended, a title, a program or a folder. tmux pushes it to a control-mode client
// on a hidden session, so the sidebar follows at once instead of at the next look. It runs
// until ctx ends, starting the client again whenever it stops (the server restarted).
func WatchLocal(ctx context.Context, changed func()) {
	wait := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		_ = watchOnce(ctx, changed)
		if time.Since(started) > time.Minute {
			wait = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if wait < 30*time.Second {
			wait *= 2
		}
	}
}

func watchOnce(ctx context.Context, changed func()) error {
	tm := LocalTmux()
	if tm == "" {
		return ErrNoLocalTmux
	}
	conf, err := localConf()
	if err != nil {
		return err
	}
	// -A attaches when the hidden session is there and makes it when it isn't. Its pane
	// runs cat, which waits quietly on input that never comes.
	cmd := exec.CommandContext(ctx, tm, "-u", "-L", localSocket(), "-f", conf, "-C", "new-session", "-A", "-s", watchSession, "-x", "80", "-y", "24", "cat")
	in, err := cmd.StdinPipe() // control mode ends when its input does: kept open
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return err
	}
	defer cmd.Wait()
	defer in.Close()
	if _, err := in.Write([]byte("refresh-client -B 'sky::" + watchFormat + "'\n")); err != nil {
		return err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "%subscription-changed"),
			strings.HasPrefix(line, "%sessions-changed"),
			strings.HasPrefix(line, "%session-renamed"),
			strings.HasPrefix(line, "%window-renamed"),
			strings.HasPrefix(line, "%window-add"),
			strings.HasPrefix(line, "%window-close"),
			strings.HasPrefix(line, "%unlinked-window"):
			changed()
		case strings.HasPrefix(line, "%exit"):
			return nil
		}
	}
	return sc.Err()
}
