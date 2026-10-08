//go:build darwin

package main

import "C"

import (
	"os"
	"sync"
	"sync/atomic"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

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
		noteErr("window %s: close button", mainApp.win.id)
	}
}

// The page hears whether its window can be seen from a goroutine, never on AppKit's
// thread: Wails' ExecJS releases the script string it makes, which is only balanced off the
// main thread (on it, the autorelease pool frees the string too, and the app crashes).
var (
	shownNow  atomic.Bool
	shownKick = make(chan struct{}, 1)
	shownOnce sync.Once
)

// skyShownChanged tells the page whether its window can be seen (see lib/shown.ts). A
// headless dev instance's window is never seen, but the page under test acts as shown.
//
//export skyShownChanged
func skyShownChanged(shown C.int) {
	if mainApp == nil || mainApp.ctx == nil || os.Getenv("SKY_HEADLESS") != "" {
		return
	}
	shownNow.Store(shown != 0)
	shownOnce.Do(func() {
		go func() {
			for range shownKick {
				wruntime.EventsEmit(mainApp.ctx, "window-shown", shownNow.Load())
			}
		}()
	})
	select {
	case shownKick <- struct{}{}:
	default: // the goroutine hasn't sent the last change yet: it will read this one
	}
}
