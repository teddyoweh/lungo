//go:build windows

package main

// There every window goes by being closed (see leaving), so none asks another to quit.
func askToQuit(int) {}

func (w *window) onQuitAsked(func()) {}
