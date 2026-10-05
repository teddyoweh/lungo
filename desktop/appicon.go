package main

import (
	"context"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"skybuild/internal/paths"
)

// The app's icon: Dawn, which the bundle carries (build/appicon.png), or one of the others
// picked in Settings. The pick is kept in ~/.skybuild/app-icon, so every window and the next
// launch use it.

//go:embed appicons/*.png
var appIcons embed.FS

const defaultIcon = "dawn"

// iconIDs are the icons in the order Settings shows them.
var iconIDs = []string{"dawn", "phosphor", "pour", "keycap", "pressed", "sugar", "redeye", "lcd", "clay", "crystal"}

func iconFile() string { return filepath.Join(paths.Root(), "app-icon") }

// chosenIcon is the icon picked in Settings, or Dawn.
func chosenIcon() string {
	b, err := os.ReadFile(iconFile())
	if id := strings.TrimSpace(string(b)); err == nil && slices.Contains(iconIDs, id) {
		return id
	}
	return defaultIcon
}

// iconPNG is an icon's 1024px picture.
func iconPNG(id string) []byte {
	if id == defaultIcon {
		return icon
	}
	b, _ := appIcons.ReadFile("appicons/" + id + ".png")
	return b
}

// shownIcon is the icon this window's process last put in the Dock.
var shownIcon struct {
	sync.Mutex
	id string
}

// showIcon puts the icon in the Dock. With bundle, it also becomes the app's icon in
// Finder, so the Dock keeps it after the app quits.
func showIcon(id string, bundle bool) {
	shownIcon.Lock()
	shownIcon.id = id
	shownIcon.Unlock()
	if id == defaultIcon {
		setDockIcon(nil, bundle)
	} else {
		setDockIcon(iconPNG(id), bundle)
	}
}

// AppIcon is the icon in use.
func (a *App) AppIcon() string { return chosenIcon() }

// SetAppIcon switches the app's icon now and for the next launch. The other windows notice
// within a couple of seconds (iconLoop).
func (a *App) SetAppIcon(id string) error {
	if !slices.Contains(iconIDs, id) {
		return fmt.Errorf("there is no icon called %q", id)
	}
	if err := os.WriteFile(iconFile(), []byte(id+"\n"), 0o600); err != nil {
		return err
	}
	showIcon(id, true)
	wruntime.EventsEmit(a.ctx, "app-icon", id)
	return nil
}

// startIcon shows the chosen icon at launch. A reinstall replaces the bundle and with it
// the Finder icon, so that is put back too when it is missing.
func startIcon() {
	id := chosenIcon()
	if id == defaultIcon {
		shownIcon.Lock()
		shownIcon.id = id // the bundle's own icon is already in the Dock
		shownIcon.Unlock()
		return
	}
	showIcon(id, !hasBundleIcon())
}

// iconLoop follows picks made in other windows: each window is its own process with its
// own Dock icon.
func (a *App) iconLoop(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		id := chosenIcon()
		shownIcon.Lock()
		same := id == shownIcon.id
		shownIcon.Unlock()
		if !same {
			showIcon(id, false)
			wruntime.EventsEmit(ctx, "app-icon", id)
		}
	}
}

// appBundle is the .app this process runs from, when it runs from one.
func appBundle() (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	i := strings.Index(exe, ".app/Contents/MacOS/")
	if i < 0 {
		return "", false
	}
	return exe[:i+len(".app")], true
}

// hasBundleIcon reports whether the bundle carries a custom Finder icon (macOS keeps it in
// a file named "Icon\r" at the top of the bundle).
func hasBundleIcon() bool {
	b, ok := appBundle()
	if !ok {
		return true // nothing to put it on
	}
	_, err := os.Stat(filepath.Join(b, "Icon\r"))
	return err == nil
}
