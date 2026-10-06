//go:build !darwin

package main

import (
	"context"
	"errors"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

func compactTitleBar() {}

func backgroundApp() {}

func debugKey(string, string, string, int, []string) {}

func debugClick(float64, float64, []string) {}

func debugStage() int { return 0 }

func debugScale(float64) {}

// activate has no portable equivalent; Alt-Tab reaches the other windows there.
func activate(int) error { return errors.New("switching windows isn't supported on this system") }

func debugSnapshot(string) {}

func debugSetFrame(frame) {}

func debugScreens() string { return "" }

func neverActivate() {}

func watchClose() {}

func wakeToQuit() {}

// setDockIcon: elsewhere the window icon is set at launch (main.go), from the pick.
func setDockIcon([]byte, bool) {}

// mainApp is the app, for callbacks that arrive from the windowing system.
var mainApp *App

const frameDown = 1

// launchFrame: here the size and state go in as window options and the position is set
// once the window exists (restoreFrame).
func launchFrame(frame, bool) {}

func currentFrame(ctx context.Context) (frame, bool) {
	x, y := wruntime.WindowGetPosition(ctx)
	w, h := wruntime.WindowGetSize(ctx)
	return frame{X: x, Y: y, W: w, H: h, Maximised: wruntime.WindowIsMaximised(ctx), Fullscreen: wruntime.WindowIsFullscreen(ctx)}, true
}
