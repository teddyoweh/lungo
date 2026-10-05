package main

import (
	"context"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"skybuild/internal/engine"
	"skybuild/internal/events"
	"skybuild/internal/paths"
)

// Projects: repos here and on machines, and moving work between them (engine/projects.go).

// Projects lists projects. With fresh=false it answers at once from the last read when there
// is one (marked stale if it is old), so the page never opens empty.
func (a *App) Projects(fresh bool) (*engine.ProjectList, error) {
	if !fresh {
		if c := a.eng.CachedProjects(); c != nil {
			return c, nil
		}
	}
	ctx, cancel := context.WithTimeout(a.ctx, 90*time.Second)
	defer cancel()
	return a.eng.Projects(ctx, true)
}

// ProjectSend continues the repo at localDir on a machine. The op's result is a
// engine.HandoffResult (where it landed, what was set aside).
func (a *App) ProjectSend(machine, localDir string, o engine.HandoffOptions) string {
	return a.ops.start("project-send", "Continue "+paths.Tilde(localDir)+" on "+machine, machine, func(ctx context.Context, r events.Reporter) (any, error) {
		res, err := a.eng.ProjectSend(ctx, machine, localDir, o, r)
		wruntime.EventsEmit(a.ctx, "projects", nil)
		if err != nil {
			return nil, err
		}
		return res, nil
	})
}

// ProjectBring brings the repo at remoteDir on a machine to this computer.
func (a *App) ProjectBring(machine, remoteDir string, o engine.HandoffOptions) string {
	return a.ops.start("project-bring", "Bring "+remoteDir+" from "+machine, machine, func(ctx context.Context, r events.Reporter) (any, error) {
		res, err := a.eng.ProjectBring(ctx, machine, remoteDir, o, r)
		wruntime.EventsEmit(a.ctx, "projects", nil)
		if err != nil {
			return nil, err
		}
		return res, nil
	})
}

// ProjectAdd remembers a local repo (picked by hand) and returns its name.
func (a *App) ProjectAdd(dir string) (string, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 20*time.Second)
	defer cancel()
	name, err := a.eng.ProjectAdd(ctx, dir)
	if err == nil {
		wruntime.EventsEmit(a.ctx, "projects", nil)
	}
	return name, err
}

// ProjectForget stops remembering a project, or with a machine just its copy there. Files
// stay where they are.
func (a *App) ProjectForget(key, machine string) error {
	err := a.eng.ProjectForget(key, machine)
	if err == nil {
		wruntime.EventsEmit(a.ctx, "projects", nil)
	}
	return err
}

// ProjectSetRemote changes the folder a local project uses on a machine.
func (a *App) ProjectSetRemote(localDir, machine, remoteDir string) error {
	ctx, cancel := context.WithTimeout(a.ctx, 20*time.Second)
	defer cancel()
	err := a.eng.ProjectSetRemote(ctx, localDir, machine, remoteDir)
	if err == nil {
		wruntime.EventsEmit(a.ctx, "projects", nil)
	}
	return err
}
