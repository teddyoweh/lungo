package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"skybuild/internal/config"
	"skybuild/internal/events"
	"skybuild/internal/folders"
	"skybuild/internal/model"
)

// Push copies a local folder or file to a machine.
func (e *Engine) Push(ctx context.Context, name, local, remote string, o folders.Options, r events.Reporter) error {
	m, err := e.Machine(name)
	if err != nil {
		return err
	}
	return folders.Push(ctx, e.Target(m), local, remote, o, events.Tagged(r, "push", name))
}

// Pull copies a folder or file from a machine.
func (e *Engine) Pull(ctx context.Context, name, remote, local string, o folders.Options, r events.Reporter) error {
	m, err := e.Machine(name)
	if err != nil {
		return err
	}
	return folders.Pull(ctx, e.Target(m), remote, local, o, events.Tagged(r, "pull", name))
}

// Clone clones a GitHub repo onto a machine with its synced gh login.
func (e *Engine) Clone(ctx context.Context, name, repo, dir string, r events.Reporter) (string, error) {
	m, err := e.Machine(name)
	if err != nil {
		return "", err
	}
	if dir == "" {
		base := repo[strings.LastIndexAny(repo, "/:")+1:]
		dir = "code/" + strings.TrimSuffix(base, ".git")
	}
	dir = folders.RemoteRel(dir)
	events.Stepf(r, "Cloning %s into %s:~/%s", repo, name, dir)
	cmd := "mkdir -p \"$(dirname " + q(dir) + ")\" && "
	if strings.Contains(repo, "://") || strings.HasPrefix(repo, "git@") {
		cmd += "git clone " + q(repo) + " " + q(dir)
	} else {
		cmd += "gh repo clone " + q(repo) + " " + q(dir)
	}
	if _, err := runOn(ctx, e, m, cmd+" 2>&1"); err != nil {
		return "", err
	}
	events.Donef(r, "Cloned to ~/%s", dir)
	return "~/" + dir, nil
}

// Links returns saved folder links.
func (e *Engine) Links() ([]*model.Link, error) {
	c, err := config.Load()
	if err != nil {
		return nil, err
	}
	return c.Links, nil
}

// AddLink saves a folder pairing.
func (e *Engine) AddLink(l model.Link) (*model.Link, error) {
	if _, err := e.Machine(l.Machine); err != nil {
		return nil, err
	}
	l.Local = folders.Expand(l.Local)
	if st, err := os.Stat(l.Local); err != nil || !st.IsDir() {
		if l.Direction != "pull" {
			return nil, fmt.Errorf("%s is not a folder", l.Local)
		}
	}
	if l.Direction == "" {
		l.Direction = "push"
	}
	if l.Remote == "" {
		l.Remote = "~/code/" + filepath.Base(l.Local)
	}
	b := make([]byte, 4)
	rand.Read(b)
	l.ID = hex.EncodeToString(b)
	err := config.Update(func(c *config.Config) error {
		c.Links = append(c.Links, &l)
		return nil
	})
	return &l, err
}

// RemoveLink deletes a folder pairing (no files are touched).
func (e *Engine) RemoveLink(id string) error {
	return config.Update(func(c *config.Config) error {
		out := c.Links[:0]
		found := false
		for _, l := range c.Links {
			if l.ID == id {
				found = true
				continue
			}
			out = append(out, l)
		}
		if !found {
			return fmt.Errorf("no link %s", id)
		}
		c.Links = out
		return nil
	})
}

// UpdateLink changes a saved link (watch on/off, excludes…).
func (e *Engine) UpdateLink(id string, fn func(*model.Link)) error {
	return config.Update(func(c *config.Config) error {
		for _, l := range c.Links {
			if l.ID == id {
				fn(l)
				return nil
			}
		}
		return fmt.Errorf("no link %s", id)
	})
}

// RunLink copies one link in its direction.
func (e *Engine) RunLink(ctx context.Context, id string, r events.Reporter) error {
	links, err := e.Links()
	if err != nil {
		return err
	}
	for _, l := range links {
		if l.ID != id {
			continue
		}
		o := folders.Options{Delete: l.Delete, Excludes: l.Excludes}
		if l.Direction == "pull" {
			err = e.Pull(ctx, l.Machine, l.Remote, l.Local, o, r)
		} else {
			err = e.Push(ctx, l.Machine, l.Local, l.Remote, o, r)
		}
		if err == nil {
			_ = e.UpdateLink(id, func(x *model.Link) { x.LastSync = time.Now() })
		}
		return err
	}
	return fmt.Errorf("no link %s", id)
}

// WatchLinks pushes push-links whenever their local folder changes, until ctx ends.
// only limits it to some link IDs (empty = links marked Watch, or all push links if none are).
func (e *Engine) WatchLinks(ctx context.Context, only []string, r events.Reporter) error {
	links, err := e.Links()
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, id := range only {
		want[id] = true
	}
	var chosen []*model.Link
	anyWatch := false
	for _, l := range links {
		anyWatch = anyWatch || l.Watch
	}
	for _, l := range links {
		if l.Direction == "pull" {
			continue
		}
		if len(want) > 0 && !want[l.ID] || len(want) == 0 && anyWatch && !l.Watch {
			continue
		}
		chosen = append(chosen, l)
	}
	if len(chosen) == 0 {
		return fmt.Errorf("no push links to watch; add one with `sky link add <folder> <machine>:<path>`")
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	owner := map[string]*model.Link{}
	skip := map[string]bool{".git": true}
	for _, ex := range append(folders.LeanExcludes, chosen[0].Excludes...) {
		skip[ex] = true
	}
	for _, l := range chosen {
		filepath.Walk(l.Local, func(p string, info os.FileInfo, err error) error {
			if err != nil || !info.IsDir() {
				return nil
			}
			if skip[info.Name()] && p != l.Local {
				return filepath.SkipDir
			}
			owner[p] = l
			return w.Add(p)
		})
		events.Infof(r, "Watching %s → %s:%s", l.Local, l.Machine, l.Remote)
	}
	// Initial copy, then debounce changes per link.
	for _, l := range chosen {
		if err := e.RunLink(ctx, l.ID, r); err != nil {
			events.Warnf(r, "%v", err)
		}
	}
	pending := map[string]*model.Link{}
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev := <-w.Events:
			l := owner[filepath.Dir(ev.Name)]
			if l == nil {
				l = owner[ev.Name]
			}
			if l == nil {
				continue
			}
			if ev.Op&fsnotify.Create != 0 {
				if st, err := os.Stat(ev.Name); err == nil && st.IsDir() && !skip[st.Name()] {
					owner[ev.Name] = l
					w.Add(ev.Name)
				}
			}
			pending[l.ID] = l
			timer.Reset(400 * time.Millisecond)
		case err := <-w.Errors:
			events.Warnf(r, "watch: %v", err)
		case <-timer.C:
			for id := range pending {
				if err := e.RunLink(ctx, id, r); err != nil {
					events.Warnf(r, "%v", err)
				}
			}
			pending = map[string]*model.Link{}
		}
	}
}

func q(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
