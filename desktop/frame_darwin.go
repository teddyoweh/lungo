//go:build darwin

package main

import "C"

// mainApp is the app, for callbacks that arrive from AppKit.
var mainApp *App

//export skyFrameChanged
func skyFrameChanged() {
	if mainApp != nil {
		mainApp.frameChanged()
	}
}

//export skyWindowClosing
func skyWindowClosing() {
	if mainApp != nil {
		mainApp.win.closing.Store(true)
	}
}
