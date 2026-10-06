//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// askToQuit asks another window to quit along with this one (SIGUSR2): it quits as the app
// quitting, so it comes back next time.
func askToQuit(pid int) { _ = syscall.Kill(pid, syscall.SIGUSR2) }

// onQuitAsked calls quit when another window asks this one to quit along.
func (w *window) onQuitAsked(quit func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR2)
	go func() {
		<-ch
		w.quitByPeer.Store(true)
		quit()
	}()
}
