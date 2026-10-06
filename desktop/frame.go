package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/gofrs/flock"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"skybuild/internal/paths"
)

// Where each window was and how big, kept in ~/.skybuild/window.json by window id, so a
// window comes back the way it was left. Coordinates are the platform's own (on macOS the
// whole desktop, so a window on a second display returns to that display).

type frame struct {
	X          int  `json:"x"`
	Y          int  `json:"y"`
	W          int  `json:"w"`
	H          int  `json:"h"`
	Maximised  bool `json:"maximised,omitempty"`
	Fullscreen bool `json:"fullscreen,omitempty"`
}

func (f frame) valid() bool { return f.W >= 400 && f.H >= 300 }

func framesFile() string { return filepath.Join(paths.Root(), "window.json") }

func readFrames() map[string]frame {
	m := map[string]frame{}
	if b, err := os.ReadFile(framesFile()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

// updateFrames changes the saved frames; every window (each its own process) writes here.
func updateFrames(change func(map[string]frame)) {
	_ = paths.Ensure()
	lock := flock.New(framesFile() + ".lock")
	if err := lock.Lock(); err != nil {
		return
	}
	defer lock.Unlock()
	m := readFrames()
	change(m)
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	tmp := framesFile() + ".tmp"
	if os.WriteFile(tmp, append(b, '\n'), 0o600) == nil {
		_ = os.Rename(tmp, framesFile())
	}
}

// startFrame is how a window opens: where it was last time; a window opened from the first
// one takes that one's size, a little down and to the right; with nothing saved (ok is
// false) it fills the screen, which is what a terminal app should do on first run.
func startFrame(id string) (f frame, ok bool) {
	saved := readFrames()
	if f = saved[id]; f.valid() {
		return f, true
	}
	if first := saved[mainWindow]; id != mainWindow && first.valid() {
		first.X += 30
		first.Y += frameDown * 30
		first.Fullscreen = false
		return first, true
	}
	return frame{}, false
}

var frameSave struct {
	sync.Mutex
	timer *time.Timer
}

// frameChanged is called whenever the window moved or changed size; the frame is saved once
// it settles. Saving as it changes (not only on quit) keeps it when the app is killed.
func (a *App) frameChanged() {
	if !a.keepsFrame() {
		return
	}
	frameSave.Lock()
	defer frameSave.Unlock()
	if frameSave.timer != nil {
		frameSave.timer.Stop()
	}
	frameSave.timer = time.AfterFunc(400*time.Millisecond, a.saveFrame)
}

func (a *App) saveFrame() {
	if !a.keepsFrame() || a.ctx == nil {
		return
	}
	f, ok := currentFrame(a.ctx)
	if !ok || !f.valid() {
		return
	}
	updateFrames(func(m map[string]frame) {
		// Full screen keeps the frame from before, so leaving full screen next time lands there.
		if prev := m[a.win.id]; f.Fullscreen && prev.valid() {
			prev.Fullscreen = true
			f = prev
		}
		m[a.win.id] = f
	})
}

// SaveWindowFrame is called by the UI after the window was resized.
func (a *App) SaveWindowFrame() { a.frameChanged() }

// keepsFrame: a hidden dev instance has no frame worth keeping (SKY_FRAME_TEST lets a dev
// check exercise the code on the hidden window anyway).
func (a *App) keepsFrame() bool {
	return os.Getenv("SKY_HEADLESS") == "" || os.Getenv("SKY_FRAME_TEST") != ""
}

// restoreFrame runs once the window exists. On macOS it is already in place (launchFrame);
// elsewhere the position is set here, the size and state having gone in as window options.
func (a *App) restoreFrame() {
	if !a.keepsFrame() || runtime.GOOS == "darwin" {
		return
	}
	if f, ok := startFrame(a.win.id); ok && !f.Maximised && !f.Fullscreen {
		wruntime.WindowSetPosition(a.ctx, max(f.X, 0), max(f.Y, 0))
	}
}

// beforeClose saves the frame one last time and settles what the window going means (see
// window.leaving); it never stops the window closing.
func (a *App) beforeClose(context.Context) bool {
	frameSave.Lock()
	if frameSave.timer != nil {
		frameSave.timer.Stop()
	}
	frameSave.Unlock()
	a.saveFrame()
	if a.win.leaving() {
		a.installOnQuit() // a downloaded update goes in as the app quits
	}
	go wakeToQuit()
	return false
}

// forgetFrames drops the frames of windows that are neither open nor to come back.
func (a *App) forgetFrames() {
	if !a.keepsFrame() || !restores() {
		return
	}
	keep := a.win.ids()
	for _, id := range readSaved().IDs {
		keep[id] = true
	}
	updateFrames(func(m map[string]frame) {
		for id := range m {
			if id != mainWindow && !keep[id] {
				delete(m, id)
			}
		}
	})
}
