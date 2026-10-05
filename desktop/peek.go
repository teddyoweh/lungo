package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"skybuild/internal/engine"
	"skybuild/internal/osx"
	"skybuild/internal/paths"
)

// Files on machines, shown here: see engine.FindFile. The page asks for a file by the name a
// session printed (or picks one from a list), the app brings it over and hands the page an
// address to load it from.

// FindFile finds the file a session means by name. dir is the pane's folder as the page
// knows it, used when the session itself can't be asked.
func (a *App) FindFile(machine, session, dir, name string) ([]engine.RemoteFile, error) {
	files, err := a.eng.FindFile(a.ctx, machine, session, dir, name)
	if files == nil {
		files = []engine.RemoteFile{}
	}
	return files, err
}

// FilesView is what was made lately where a session works.
type FilesView struct {
	Dir   string              `json:"dir"`
	Files []engine.RemoteFile `json:"files"`
}

// RecentFiles lists what was made or changed lately where a session works.
func (a *App) RecentFiles(machine, session, dir string) (FilesView, error) {
	dir, files, err := a.eng.RecentFiles(a.ctx, machine, session, dir)
	if files == nil {
		files = []engine.RemoteFile{}
	}
	return FilesView{Dir: dir, Files: files}, err
}

// ListDir lists a folder on a machine.
func (a *App) ListDir(machine, dir string) ([]engine.RemoteFile, error) {
	files, err := a.eng.ListDir(a.ctx, machine, dir)
	if files == nil {
		files = []engine.RemoteFile{}
	}
	return files, err
}

// PeekView is a file brought over and ready to show.
type PeekView struct {
	File  engine.RemoteFile `json:"file"`
	Local string            `json:"local"` // where it is on this computer
	URL   string            `json:"url"`   // where the page loads it from
}

// PeekFile brings a file over from a machine and makes it loadable by the page. While it
// comes, "peek:progress" says how far it is.
func (a *App) PeekFile(machine string, f engine.RemoteFile) (PeekView, error) {
	if a.terms == nil {
		return PeekView{}, errors.New("the app's local server isn't running")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 20*time.Minute)
	defer cancel()
	local, err := a.eng.FetchFile(ctx, machine, f, func(done int64) {
		wruntime.EventsEmit(a.ctx, "peek:progress", map[string]any{"machine": machine, "path": f.Path, "done": done, "size": f.Size})
	})
	if err != nil {
		return PeekView{}, err
	}
	return PeekView{File: f, Local: local, URL: a.terms.ShareFile(local)}, nil
}

// peeked checks that a path is a file the page was shown: the bindings below act on files
// on this computer, and only on those.
func (a *App) peeked(path string) error {
	if a.terms == nil || !a.terms.Shared(path) {
		return errors.New("not a file that was opened here")
	}
	return nil
}

// OpenPeeked opens a fetched file with the app this computer uses for it.
func (a *App) OpenPeeked(path string) error {
	if err := a.peeked(path); err != nil {
		return err
	}
	return osx.OpenURL(path)
}

// RevealPeeked shows a fetched file in the file manager.
func (a *App) RevealPeeked(path string) error {
	if err := a.peeked(path); err != nil {
		return err
	}
	return osx.RevealFile(path)
}

// SavePeeked copies a fetched file to Downloads (under a free name) and shows it there.
func (a *App) SavePeeked(path string) (string, error) {
	if err := a.peeked(path); err != nil {
		return "", err
	}
	dir := filepath.Join(paths.Home(), "Downloads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := filepath.Base(path)
	ext := filepath.Ext(name)
	dst := filepath.Join(dir, name)
	for i := 2; ; i++ {
		if _, err := os.Lstat(dst); err != nil {
			break
		}
		dst = filepath.Join(dir, fmt.Sprintf("%s %d%s", strings.TrimSuffix(name, ext), i, ext))
	}
	in, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	_ = osx.RevealFile(dst)
	return dst, nil
}
