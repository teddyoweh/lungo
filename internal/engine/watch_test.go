package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

// tmux tells the watcher about a session made and one ended, and the watcher's own session
// is never listed.
func TestWatchLocal(t *testing.T) {
	if LocalTmux() == "" {
		t.Skip("tmux is not installed")
	}
	t.Setenv("SKYBUILD_HOME", t.TempDir())
	sock := fmt.Sprintf("skywatch-%d", os.Getpid())
	t.Setenv("SKY_TMUX_SOCKET", sock)
	defer exec.Command(LocalTmux(), "-L", sock, "kill-server").Run()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes := make(chan struct{}, 100)
	go WatchLocal(ctx, func() { changes <- struct{}{} })
	wait := func(what string) {
		t.Helper()
		select {
		case <-changes:
		case <-time.After(5 * time.Second):
			t.Fatalf("no change reported after %s", what)
		}
	}
	time.Sleep(time.Second) // the watcher's client is up
	for len(changes) > 0 {
		<-changes
	}
	if out, err := exec.Command(LocalTmux(), "-L", sock, "new-session", "-d", "-s", "shell-w1", "sleep 60").CombinedOutput(); err != nil {
		t.Fatalf("new-session: %v %s", err, out)
	}
	wait("a session was made")
	list, err := New().LocalSessions(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "shell-w1" {
		t.Fatalf("sessions = %v (%v), want only shell-w1", names(list), err)
	}
	for len(changes) > 0 {
		<-changes
	}
	exec.Command(LocalTmux(), "-L", sock, "kill-session", "-t", "=shell-w1").Run()
	wait("a session ended")
}
