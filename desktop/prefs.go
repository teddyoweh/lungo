package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"skybuild/internal/paths"
)

// The page's own settings (session names and colours, workspaces, theme, terminal look,
// each window's zoom and sidebar, …) are kept here, in ~/.skybuild/state/prefs.json, and
// not only in the page's storage: windows are separate processes, and only one of them gets
// its page storage written to disk, so what any other window saved was gone after a
// restart. Each window copies these into its page storage before the page starts, sends
// what it changes, and hears what the others change ("prefs" events).

func prefsFile() string { return filepath.Join(paths.State(), "prefs.json") }

func readPrefs() map[string]string {
	m := map[string]string{}
	if b, err := os.ReadFile(prefsFile()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

// writePrefs applies changes (an empty value deletes the key) under a lock, so windows
// writing at once lose nothing. It reports whether the file changed.
func writePrefs(changes map[string]string) (bool, error) {
	if len(changes) == 0 {
		return false, nil
	}
	if err := os.MkdirAll(paths.State(), 0o700); err != nil {
		return false, err
	}
	lock := flock.New(prefsFile() + ".lock")
	if err := lock.Lock(); err != nil {
		return false, err
	}
	defer lock.Unlock()
	m := readPrefs()
	for k, v := range changes {
		if v == "" {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	b, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return false, err
	}
	return paths.WriteFile(prefsFile(), b, 0o600)
}

// Prefs is every setting the pages have saved.
func (a *App) Prefs() map[string]string { return readPrefs() }

// SetPrefs saves settings, many in one write (an empty value removes one).
func (a *App) SetPrefs(changes map[string]string) error {
	if os.Getenv("SKY_HEADLESS") != "" && os.Getenv("SKYBUILD_HOME") == "" {
		return nil // a hidden dev window doesn't write the user's own settings
	}
	_, err := writePrefs(changes)
	return err
}

var prefsSeen struct {
	sync.Mutex
	mod  time.Time
	size int64
}

// watchPrefs tells the page when another window changed a setting.
func (a *App) watchPrefs(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		fi, err := os.Stat(prefsFile())
		if err != nil {
			continue
		}
		prefsSeen.Lock()
		changed := !fi.ModTime().Equal(prefsSeen.mod) || fi.Size() != prefsSeen.size
		first := prefsSeen.mod.IsZero()
		prefsSeen.mod, prefsSeen.size = fi.ModTime(), fi.Size()
		prefsSeen.Unlock()
		if changed && !first {
			wruntime.EventsEmit(ctx, "prefs", readPrefs())
		}
	}
}
