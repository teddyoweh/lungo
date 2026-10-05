package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/pprof"
	"sync"
	"syscall"
	"time"

	"skybuild/internal/paths"
)

// What the app leaves behind when something goes wrong. Opened from Finder or at login, its
// error output goes nowhere, so a stall or a crash would leave no trace: it goes to a log
// instead, and the state of every part of the app (its goroutines) can be written out on
// request (kill -USR1 <pid>) or by the app itself when a refresh stalls.

func logDir() string { return filepath.Join(paths.Root(), "logs") }

// keepErrors sends the app's error output to ~/.skybuild/logs/lungo.log when nothing else
// receives it. The log starts afresh once it passes 2 MB.
func keepErrors() {
	st, err := os.Stderr.Stat()
	if err != nil || st.Mode()&os.ModeCharDevice == 0 || os.Getenv("SKY_HEADLESS") != "" {
		return // a terminal or a file already has it (a dev run)
	}
	if fi, err := os.Stat("/dev/null"); err != nil || !os.SameFile(fi, st) {
		return
	}
	_ = os.MkdirAll(logDir(), 0o700)
	path := filepath.Join(logDir(), "lungo.log")
	if fi, err := os.Stat(path); err == nil && fi.Size() > 2<<20 {
		_ = os.Rename(path, path+".old")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_ = syscall.Dup2(int(f.Fd()), 2)
	fmt.Fprintf(os.Stderr, "\n--- Lungo started %s (pid %d)\n", time.Now().Format(time.RFC3339), os.Getpid())
}

// noteErr writes one line to the app's log.
func noteErr(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

var stacksMu sync.Mutex
var stacksAt time.Time

// writeStacks writes what every goroutine is doing to ~/.skybuild/logs/lungo-stacks-<time>.txt.
// When the app does it on its own (a stall), at most once every ten minutes.
func writeStacks(why string, onDemand bool) {
	stacksMu.Lock()
	defer stacksMu.Unlock()
	if !onDemand && time.Since(stacksAt) < 10*time.Minute {
		return
	}
	stacksAt = time.Now()
	_ = os.MkdirAll(logDir(), 0o700)
	path := filepath.Join(logDir(), "lungo-stacks-"+time.Now().Format("20060102-150405")+".txt")
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s\n%s\n\n", why, time.Now().Format(time.RFC3339))
	_ = pprof.Lookup("goroutine").WriteTo(f, 2)
	noteErr("wrote %s (%s)", filepath.Base(path), why)
}

// stacksOnSignal writes the goroutines out on kill -USR1.
func stacksOnSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR1)
	go func() {
		for range ch {
			writeStacks("asked for (SIGUSR1)", true)
		}
	}()
}
