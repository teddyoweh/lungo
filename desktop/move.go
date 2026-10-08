package main

import (
	"context"
	"path"
	"sort"
	"strings"
	"time"

	"skybuild/internal/engine"
	"skybuild/internal/events"
)

// Folders across devices, and sending one to another device as it is (engine.MoveFolder).

// FolderActivity is a folder where Claude worked lately, on one device.
type FolderActivity struct {
	Machine       string    `json:"machine"`
	Dir           string    `json:"dir"` // absolute, on that device
	Name          string    `json:"name"`
	Updated       time.Time `json:"updated"` // the last thing said in a conversation there
	Conversations int       `json:"conversations"`
	Latest        string    `json:"latest"`      // the most recent conversation
	LatestTitle   string    `json:"latestTitle"` // what it is about
	Branch        string    `json:"branch,omitempty"`
	Live          []string  `json:"live,omitempty"` // tmux sessions open in it right now
}

// RecentFoldersView is the recent folders of every device that answered.
type RecentFoldersView struct {
	Folders []FolderActivity  `json:"folders"`
	Errors  map[string]string `json:"errors"`
}

// throwaway are folders no one means to come back to: scratch space and temporary folders.
func throwaway(dir string) bool {
	return dir == "" || strings.HasPrefix(dir, "/tmp/") || strings.HasPrefix(dir, "/private/tmp/") || strings.HasPrefix(dir, "/private/var/folders/") || strings.HasPrefix(dir, "/var/folders/") || strings.Contains(dir, "/scratchpad")
}

// RecentFolders lists the folders you work in on every device, the most recent first: where
// Claude conversations ran (from each device's Claude history, so they are there whether or
// not a session is open in them now), and the git repos found here and on the machines,
// ranked by their last commit. fresh looks for repos again rather than using the last look.
func (a *App) RecentFolders(fresh bool) RecentFoldersView {
	ctx, cancel := context.WithTimeout(a.ctx, 25*time.Second)
	defer cancel()
	var projects *engine.ProjectList
	repos := make(chan struct{})
	go func() {
		defer close(repos)
		if !fresh {
			projects = a.eng.CachedProjects()
		}
		if projects == nil {
			projects, _ = a.eng.Projects(ctx, fresh)
		}
	}()
	list, errs := a.eng.AllHistory(ctx, engine.HistoryOptions{Limit: 400, Auto: -1, Sessions: a.poll.publishedSessions()})
	type key struct{ m, d string }
	by := map[key]*FolderActivity{}
	for _, c := range list {
		if throwaway(c.Dir) || c.Auto {
			continue
		}
		k := key{c.Machine, c.Dir}
		f := by[k]
		if f == nil {
			f = &FolderActivity{Machine: c.Machine, Dir: c.Dir, Name: path.Base(c.Dir)}
			by[k] = f
		}
		f.Conversations++
		if c.Updated.After(f.Updated) {
			f.Updated, f.Latest, f.LatestTitle, f.Branch = c.Updated, c.ID, c.Title, c.Branch
		}
		if c.Live != "" && !contains(f.Live, c.Live) {
			f.Live = append(f.Live, c.Live)
		}
	}
	<-repos
	if projects != nil {
		add := func(machine string, side *engine.ProjectSide) {
			if side == nil || side.Path == "" || throwaway(side.Path) {
				return
			}
			k := key{machine, side.Path}
			if f := by[k]; f != nil {
				return // there already, by its conversations
			}
			by[k] = &FolderActivity{Machine: machine, Dir: side.Path, Name: path.Base(side.Path), Updated: side.CommitAt, Branch: side.Branch, LatestTitle: side.Subject}
		}
		for _, p := range projects.Projects {
			add(engine.LocalMachine, p.Local)
			for _, c := range p.Copies {
				add(c.Machine, c.Side)
			}
		}
	}
	v := RecentFoldersView{Folders: []FolderActivity{}, Errors: errs}
	for _, f := range by {
		v.Folders = append(v.Folders, *f)
	}
	sort.Slice(v.Folders, func(i, j int) bool { return v.Folders[i].Updated.After(v.Folders[j].Updated) })
	if v.Errors == nil {
		v.Errors = map[string]string{}
	}
	return v
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// PlanMove says what sending a folder to another device would do.
func (a *App) PlanMove(from, to, dir string) (engine.MovePlan, error) {
	return a.eng.PlanMove(a.ctx, from, to, dir)
}

// MoveFolder sends a folder to another device as an operation; its result is the
// engine.MoveResult.
func (a *App) MoveFolder(from, to, dir string) string {
	name := path.Base(dir)
	where := to
	if engine.IsLocal(to) {
		where = "this computer"
	}
	return a.ops.start("move", "Send "+name+" to "+where, to, func(ctx context.Context, r events.Reporter) (any, error) {
		res, err := a.eng.MoveFolder(ctx, from, to, dir, r)
		if err != nil {
			return nil, err
		}
		return res, nil
	})
}

// MoveSession moves the session in a pane to another device as an operation (see
// engine.MoveSession): it stops where it is, its folder and conversations go over, and the
// pane picks the conversation up there. The result is the engine.MoveResult.
func (a *App) MoveSession(from, session, to, dir string) string {
	where := to
	if engine.IsLocal(to) {
		where = "this computer"
	}
	return a.ops.start("move", "Move "+path.Base(dir)+" to "+where, to, func(ctx context.Context, r events.Reporter) (any, error) {
		res, err := a.eng.MoveSession(ctx, from, session, to, dir, r)
		go a.refreshSessions(from)
		if err != nil {
			return nil, err
		}
		return res, nil
	})
}

// AgentsOn lists the coding agents installed on a machine ("@local": this computer), for
// the new-session dialog to offer.
func (a *App) AgentsOn(machine string) []string {
	ctx, cancel := context.WithTimeout(a.ctx, 12*time.Second)
	defer cancel()
	list, _ := a.eng.AgentsOn(ctx, machine)
	if list == nil {
		list = []string{}
	}
	return list
}

// AgentVersions lists the coding agents installed on a machine ("@local": this computer) with
// the version each reports, for the welcome.
func (a *App) AgentVersions(machine string) []engine.AgentVersion {
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
	defer cancel()
	list, _ := a.eng.AgentVersions(ctx, machine)
	if list == nil {
		list = []engine.AgentVersion{}
	}
	return list
}
