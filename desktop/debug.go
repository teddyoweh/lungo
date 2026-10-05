package main

import (
	"sync"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Debug is bound only in a headless dev run (SKY_HEADLESS). The UI is normally checked from a
// headless browser, but a browser has no native menu and isn't WKWebView, so shortcuts and
// zoom also need checking in the app's own window. That window stays hidden; these calls
// let a dev check drive it: run script in it, read values back, and send it real key events
// through AppKit (the same path a key press takes: web view first, then the menu).
type Debug struct {
	a    *App
	mu   sync.Mutex
	vals map[string]string
}

// Exec runs script in the app's own web view.
func (d *Debug) Exec(js string) { wruntime.WindowExecJS(d.a.ctx, js) }

// Put is called by that script to hand a value back.
func (d *Debug) Put(key, value string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.vals == nil {
		d.vals = map[string]string{}
	}
	d.vals[key] = value
}

// Get returns a value handed back with Put.
func (d *Debug) Get(key string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.vals[key]
}

// Key sends a key press to the app's window. chars is what the key types with the modifiers
// held ("+" for ⌘⇧=), plain what it types without them ("="), code the virtual key code;
// mods are "cmd", "shift", "alt", "ctrl". kind is "press" (down and up), or "flags" for a
// modifier going down or up on its own.
func (d *Debug) Key(kind, chars, plain string, code int, mods []string) {
	debugKey(kind, chars, plain, code, mods)
}

// Click clicks at (x, y) in the page (CSS pixels from the top left), with modifiers held.
func (d *Debug) Click(x, y float64, mods []string) { debugClick(x, y, mods) }

// Stage puts the hidden window where the system draws it but no screen shows it, so the web
// view lays out and animates as it would on screen. It returns the window's number, for
// capturing that window alone.
func (d *Debug) Stage() int { return debugStage() }

// Scale makes the page render as on a display with this scale factor (2 for Retina).
func (d *Debug) Scale(f float64) { debugScale(f) }

// Snapshot writes a picture of what the web view shows to path (a PNG).
func (d *Debug) Snapshot(path string) { debugSnapshot(path) }

// Frame is the window's frame right now; SetFrame moves and sizes it as a drag would;
// Screens lists the usable area of each display. SavedFrames is what window.json holds.
func (d *Debug) Frame() frame {
	f, _ := currentFrame(d.a.ctx)
	return f
}

func (d *Debug) SetFrame(x, y, w, h int) { debugSetFrame(frame{X: x, Y: y, W: w, H: h}) }

func (d *Debug) Screens() string { return debugScreens() }

func (d *Debug) SavedFrames() map[string]frame { return readFrames() }
