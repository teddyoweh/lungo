package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"skybuild/internal/config"
	"skybuild/internal/events"
	"skybuild/internal/model"
	"skybuild/internal/osx"
	"skybuild/internal/paths"
	"skybuild/internal/projects"
	"skybuild/internal/sshx"
)

// Projects are git repos that live on this computer, on machines, or both. Sending one to a
// machine (or bringing it back) reproduces its exact working state there: see package
// projects for how.

// remoteExec runs sh scripts on a machine (starting in its home folder) with stdin and
// stdout attached, for the git plumbing in package projects.
func (e *Engine) remoteExec(m *model.Machine) projects.Exec {
	t := e.Target(m)
	return func(ctx context.Context, script string, stdin io.Reader, stdout io.Writer) error {
		args := append(t.Options(true), t.Dest(), "--", sshx.RemotePath+"sh -c "+sshx.Quote(script))
		cmd := exec.CommandContext(ctx, osx.Which("ssh"), args...)
		cmd.Stdin, cmd.Stdout = stdin, stdout
		var errb bytes.Buffer
		cmd.Stderr = &errb
		if err := cmd.Run(); err != nil {
			return projects.ScriptError(err, cleanSSHNoise(errb.String()))
		}
		return nil
	}
}

func cleanSSHNoise(s string) string {
	var keep []string
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, "Permanently added") || strings.Contains(l, "post-quantum") || strings.Contains(l, "disabling multiplexing") || strings.Contains(l, "mux_client_request_session") {
			continue
		}
		keep = append(keep, l)
	}
	return strings.Join(keep, "\n")
}

// transport is how this computer's git reaches a repo folder on a machine.
func (e *Engine) transport(m *model.Machine, dir string) projects.Transport {
	t := e.Target(m)
	p := dir
	switch {
	case p == "~":
		p = "."
	case strings.HasPrefix(p, "~/"):
		p = p[2:]
	}
	return projects.Transport{URL: t.Dest() + ":" + p, Env: []string{"GIT_SSH_COMMAND=" + t.SSHCommand()}}
}

// tildeIn shows a path under home as ~/….
func tildeIn(home, p string) string {
	if home != "" && (p == home || strings.HasPrefix(p, home+"/")) {
		return "~" + p[len(home):]
	}
	return p
}

// ProjectSide is one copy of a project: here, or on a machine.
type ProjectSide struct {
	Dir         string    `json:"dir"`  // shown: ~/code/api
	Path        string    `json:"path"` // absolute on that side
	Branch      string    `json:"branch"`
	Head        string    `json:"head"` // short
	Subject     string    `json:"subject"`
	CommitAt    time.Time `json:"commitAt"`
	HasUpstream bool      `json:"hasUpstream"`
	Ahead       int       `json:"ahead"`
	Behind      int       `json:"behind"`
	Changed     int       `json:"changed"`
	Untracked   int       `json:"untracked"`
}

func sideOf(s *projects.State, home string) *ProjectSide {
	if s == nil {
		return nil
	}
	head := s.Head
	if len(head) > 7 {
		head = head[:7]
	}
	return &ProjectSide{Dir: tildeIn(home, s.Dir), Path: s.Dir, Branch: s.Branch, Head: head, Subject: s.Subject, CommitAt: s.CommitAt,
		HasUpstream: s.HasUpstream, Ahead: s.Ahead, Behind: s.Behind, Changed: s.Changed, Untracked: s.Untracked}
}

// ProjectCopy is a project's copy on one machine and how it relates to the copy here.
type ProjectCopy struct {
	Machine       string       `json:"machine"`
	Side          *ProjectSide `json:"side"`   // nil: remembered there but not found (or unreachable)
	State         string       `json:"state"`  // synced | local-ahead | remote-ahead | diverged | only-remote | missing | unreachable
	Text          string       `json:"text"`   // the same, in words
	Action        string       `json:"action"` // send | bring | open | ""
	LocalCommits  int          `json:"localCommits"`
	RemoteCommits int          `json:"remoteCommits"`
}

// ProjectView is one project across this computer and the machines.
type ProjectView struct {
	ID     string        `json:"id"`
	Name   string        `json:"name"`
	Folder string        `json:"folder"` // folder name when it differs from the repo's name ("" otherwise)
	Origin string        `json:"origin"` // github.com/owner/repo, "" without a remote
	Host   string        `json:"host"`   // github | gitlab | bitbucket | git | ""
	Local  *ProjectSide  `json:"local"`
	Copies []ProjectCopy `json:"copies"`
	Saved  bool          `json:"saved"` // remembered in settings (sent, brought or added by hand)
}

// ProjectList is what the Projects page shows.
type ProjectList struct {
	Projects []ProjectView     `json:"projects"`
	Machines []string          `json:"machines"` // machines that could be read
	Errors   map[string]string `json:"errors"`   // machine → why it couldn't be read
	Roots    []string          `json:"roots"`    // folders scanned on this computer
	At       time.Time         `json:"at"`
	Stale    bool              `json:"stale"` // from the last run, shown while a fresh read is under way
}

func projectsCachePath() string { return filepath.Join(paths.State(), "projects.json") }

// CachedProjects returns the list from the last read (even from a previous run), so the page
// can show something at once. Nil when there has never been one.
func (e *Engine) CachedProjects() *ProjectList {
	projectsMu.Lock()
	defer projectsMu.Unlock()
	if projectsCache != nil {
		c := *projectsCache
		c.Stale = time.Since(c.At) > 6*time.Second
		return &c
	}
	b, err := os.ReadFile(projectsCachePath())
	if err != nil {
		return nil
	}
	var l ProjectList
	if json.Unmarshal(b, &l) != nil || l.Projects == nil {
		return nil
	}
	l.Stale = true
	return &l
}

func hostOf(origin string) string {
	switch {
	case origin == "":
		return ""
	case strings.HasPrefix(origin, "github.com/"):
		return "github"
	case strings.HasPrefix(origin, "gitlab.com/"):
		return "gitlab"
	case strings.HasPrefix(origin, "bitbucket.org/"):
		return "bitbucket"
	}
	return "git"
}

func projectKey(origin, dir string) string {
	if n := projects.NormalizeOrigin(origin); n != "" {
		return strings.ToLower(n)
	}
	return "name:" + strings.ToLower(filepath.Base(strings.TrimRight(dir, "/")))
}

func (e *Engine) projectRoots(c *config.Config) []string {
	if len(c.Settings.ProjectRoots) > 0 {
		var out []string
		for _, r := range c.Settings.ProjectRoots {
			out = append(out, paths.Expand(r))
		}
		return out
	}
	return projects.DefaultRoots(paths.Home())
}

var (
	projectsMu    sync.Mutex
	projectsCache *ProjectList
)

// localStates reads repos on this computer a few at a time in parallel (git status on a big
// repo takes a moment, and there can be dozens).
func localStates(ctx context.Context, dirs []string, peers []string, trees bool) []projects.State {
	const workers = 6
	chunks := make([][]string, workers)
	for i, d := range dirs {
		chunks[i%workers] = append(chunks[i%workers], d)
	}
	out := make([][]projects.State, workers)
	var wg sync.WaitGroup
	for i, ch := range chunks {
		if len(ch) == 0 {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i], _ = projects.Status(ctx, projects.LocalExec(), ch, peers, trees)
		}()
	}
	wg.Wait()
	var all []projects.State
	for _, o := range out {
		all = append(all, o...)
	}
	return all
}

// Projects lists every project with where each copy stands. fresh skips the short cache.
func (e *Engine) Projects(ctx context.Context, fresh bool) (*ProjectList, error) {
	projectsMu.Lock()
	if c := projectsCache; c != nil && !fresh && time.Since(c.At) < 6*time.Second {
		projectsMu.Unlock()
		return c, nil
	}
	projectsMu.Unlock()

	c, err := config.Load()
	if err != nil {
		return nil, err
	}
	home := paths.Home()
	roots := e.projectRoots(c)
	list := &ProjectList{Errors: map[string]string{}, Machines: []string{}, Projects: []ProjectView{}, Roots: []string{}, At: time.Now()}
	for _, r := range roots {
		list.Roots = append(list.Roots, paths.Tilde(r))
	}

	// This computer: scanned repos plus remembered ones.
	dirs := projects.FindLocal(roots, 60)
	have := map[string]bool{}
	for _, d := range dirs {
		have[d] = true
	}
	for _, p := range c.Projects {
		if p.Local != "" && !have[p.Local] {
			if _, err := os.Stat(filepath.Join(p.Local, ".git")); err == nil {
				dirs = append(dirs, p.Local)
				have[p.Local] = true
			}
		}
	}
	locals := localStates(ctx, dirs, nil, false)
	localByDir := map[string]*projects.State{}
	var heads []string
	for i := range locals {
		s := &locals[i]
		localByDir[canonical(s.Dir)] = s
		if s.Head != "" {
			heads = append(heads, s.Head)
		}
	}

	// Machines: one round trip each finds their repos and compares them with our heads.
	type remote struct {
		states []projects.State
		home   string
	}
	remotes := map[string]*remote{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, m := range c.Machines {
		if m.Status == model.StatusStopped || m.Status == model.StatusMissing {
			continue
		}
		var extra []string
		for _, p := range c.Projects {
			if d := p.Remotes[m.Name]; d != "" {
				extra = append(extra, d)
			}
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
			defer cancel()
			states, rhome, err := projects.Discover(cctx, e.remoteExec(m), extra, heads, true)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				list.Errors[m.Name] = firstLine(err.Error())
				return
			}
			remotes[m.Name] = &remote{states, rhome}
			list.Machines = append(list.Machines, m.Name)
		}()
	}
	wg.Wait()
	sort.Strings(list.Machines)

	// Assemble: remembered projects first (their folders are explicit), then match the rest
	// by origin, falling back to folder name.
	views := map[string]*ProjectView{}
	var order []string
	localKey := map[string]string{} // canonical local dir → view id
	get := func(id, name, origin string) *ProjectView {
		if v := views[id]; v != nil {
			return v
		}
		n := projects.NormalizeOrigin(origin)
		v := &ProjectView{ID: id, Name: name, Origin: n, Host: hostOf(n), Copies: []ProjectCopy{}}
		views[id] = v
		order = append(order, id)
		return v
	}
	for i := range locals {
		s := &locals[i]
		id := projectKey(s.Origin, s.Dir)
		if v := views[id]; v != nil && v.Local != nil { // a second clone of the same repo
			id += "#" + s.Dir
		}
		v := get(id, projects.Name(s.Origin, s.Dir), s.Origin)
		v.Local = sideOf(s, home)
		if base := filepath.Base(s.Dir); base != v.Name {
			v.Folder = base
		}
		localKey[canonical(s.Dir)] = id
	}
	saved := map[string]*config.Project{} // view id → remembered project
	for _, p := range c.Projects {
		id := ""
		if p.Local != "" {
			id = localKey[canonical(p.Local)]
		}
		if id == "" {
			id = strings.ToLower(p.Origin)
			if id == "" {
				id = "name:" + strings.ToLower(p.Name)
			}
		}
		v := get(id, p.Name, p.Origin)
		v.Saved = true
		saved[id] = p
	}

	type pair struct {
		view    *ProjectView
		machine string
		remote  *projects.State
		base    string
	}
	var pairs []pair
	for _, name := range list.Machines {
		r := remotes[name]
		used := map[int]bool{}
		// Remembered folders first.
		for id, p := range saved {
			want := p.Remotes[name]
			if want == "" {
				continue
			}
			found := false
			for i := range r.states {
				if tildeIn(r.home, r.states[i].Dir) == want || r.states[i].Dir == want {
					pairs = append(pairs, pair{views[id], name, &r.states[i], p.Base[name]})
					used[i], found = true, true
					break
				}
			}
			if !found {
				views[id].Copies = append(views[id].Copies, ProjectCopy{Machine: name, State: "missing", Text: want + " is no longer on " + name, Action: "send", LocalCommits: -1, RemoteCommits: -1})
			}
		}
		for i := range r.states {
			if used[i] {
				continue
			}
			s := &r.states[i]
			id := projectKey(s.Origin, s.Dir)
			v := views[id]
			if v == nil {
				v = get(id, projects.Name(s.Origin, s.Dir), s.Origin)
			}
			already := false
			for _, p := range pairs {
				if p.view == v && p.machine == name {
					already = true
				}
			}
			if already {
				continue
			}
			pairs = append(pairs, pair{v, name, s, ""})
		}
	}
	for id, p := range saved { // remembered on a machine we couldn't read
		for name := range p.Remotes {
			if _, bad := list.Errors[name]; bad {
				views[id].Copies = append(views[id].Copies, ProjectCopy{Machine: name, State: "unreachable", Text: "Can't reach " + name + " right now", LocalCommits: -1, RemoteCommits: -1})
			}
		}
	}

	// Projects that exist on both sides need this computer's tree and its view of the
	// machine's head.
	var both []string
	var remoteHeads []string
	for _, p := range pairs {
		if p.view.Local != nil {
			both = append(both, p.view.Local.Path)
			if p.remote.Head != "" {
				remoteHeads = append(remoteHeads, p.remote.Head)
			}
		}
	}
	detailed := map[string]*projects.State{}
	if len(both) > 0 {
		ds := localStates(ctx, both, remoteHeads, true)
		for i := range ds {
			detailed[canonical(ds[i].Dir)] = &ds[i]
		}
	}
	for _, p := range pairs {
		var local *projects.State
		if p.view.Local != nil {
			local = detailed[canonical(p.view.Local.Path)]
			if local == nil {
				local = localByDir[canonical(p.view.Local.Path)]
			}
		}
		rel := projects.Relate(local, p.remote, p.base)
		p.view.Copies = append(p.view.Copies, ProjectCopy{
			Machine: p.machine, Side: sideOf(p.remote, remotes[p.machine].home), State: rel.State,
			Text: rel.Describe(p.machine), Action: rel.Action(), LocalCommits: rel.LocalCommits, RemoteCommits: rel.RemoteCommits,
		})
	}

	for _, id := range order {
		v := views[id]
		sort.Slice(v.Copies, func(i, j int) bool { return v.Copies[i].Machine < v.Copies[j].Machine })
		list.Projects = append(list.Projects, *v)
	}
	recent := func(v ProjectView) time.Time {
		t := time.Time{}
		if v.Local != nil {
			t = v.Local.CommitAt
		}
		for _, cp := range v.Copies {
			if cp.Side != nil && cp.Side.CommitAt.After(t) {
				t = cp.Side.CommitAt
			}
		}
		return t
	}
	rank := func(v ProjectView) int { // on machines or remembered first
		if len(v.Copies) > 0 || v.Saved {
			return 0
		}
		return 1
	}
	sort.SliceStable(list.Projects, func(i, j int) bool {
		a, b := list.Projects[i], list.Projects[j]
		if rank(a) != rank(b) {
			return rank(a) < rank(b)
		}
		return recent(a).After(recent(b))
	})

	projectsMu.Lock()
	projectsCache = list
	projectsMu.Unlock()
	if b, err := json.Marshal(list); err == nil && paths.Ensure() == nil {
		_, _ = paths.WriteFile(projectsCachePath(), b, 0o600)
	}
	return list, nil
}

func canonical(dir string) string {
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		return real
	}
	return filepath.Clean(dir)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func invalidateProjects() {
	projectsMu.Lock()
	projectsCache = nil // the copy on disk stays, to show (as stale) until the next read
	projectsMu.Unlock()
}

// HandoffOptions tune a send or a bring.
type HandoffOptions struct {
	To string `json:"to,omitempty"` // destination folder (default: remembered, found by origin, or ~/code/<name>)
}

// HandoffResult is what a send or bring did.
type HandoffResult struct {
	Name      string   `json:"name"`
	Machine   string   `json:"machine"`
	LocalDir  string   `json:"localDir"`  // absolute, on this computer
	RemoteDir string   `json:"remoteDir"` // ~/… on the machine
	Branch    string   `json:"branch"`
	Created   string   `json:"created"` // "" | cloned | init: how the destination repo came to exist
	Stashed   bool     `json:"stashed"` // the destination had uncommitted work; it is in its stash
	Backup    string   `json:"backup"`  // branch holding destination commits the incoming branch lacked
	Files     []string `json:"files"`   // ignored files copied (.env…)
}

// Summary is one sentence for a toast or the last line of the CLI.
func (r HandoffResult) Summary(sent bool) string {
	where := r.Machine + " (" + r.RemoteDir + ")"
	if !sent {
		where = "this Mac (" + paths.Tilde(r.LocalDir) + ")"
	}
	s := r.Name + " is on " + where
	if r.Branch != "" {
		s += ", on " + r.Branch
	}
	var notes []string
	if r.Stashed {
		notes = append(notes, "uncommitted work that was there is in `git stash`")
	}
	if r.Backup != "" {
		notes = append(notes, "commits that were only there are on "+r.Backup)
	}
	if len(notes) > 0 {
		s += ". Kept: " + strings.Join(notes, "; ")
	}
	return s
}

func localOrigin(ctx context.Context, dir string) string {
	out, _ := osx.Run(ctx, "git", "-C", dir, "config", "--get", "remote.origin.url")
	return strings.TrimSpace(out)
}

// rememberProject records where a project lives and the tree both sides now share.
func rememberProject(name, origin, local, machine, remoteDir, tree string) error {
	defer invalidateProjects()
	return config.Update(func(c *config.Config) error {
		var p *config.Project
		for _, x := range c.Projects {
			if (local != "" && x.Local != "" && canonical(x.Local) == canonical(local)) ||
				(x.Local == "" && origin != "" && x.Origin == origin) || (x.Local == "" && origin == "" && x.Origin == "" && x.Name == name) {
				p = x
				break
			}
		}
		if p == nil {
			p = &config.Project{Name: name, Added: time.Now()}
			c.Projects = append(c.Projects, p)
		}
		p.Name, p.Origin = name, origin
		if local != "" {
			p.Local = local
		}
		if machine != "" {
			if p.Remotes == nil {
				p.Remotes = map[string]string{}
			}
			if p.Base == nil {
				p.Base = map[string]string{}
			}
			p.Remotes[machine] = remoteDir
			if tree != "" {
				p.Base[machine] = tree
			}
		}
		return nil
	})
}

// baseTree is the tree a local folder and a machine folder shared after their last handoff.
func baseTree(local, machine, remoteDir string) string {
	c, err := config.Load()
	if err != nil {
		return ""
	}
	for _, p := range c.Projects {
		if p.Local != "" && canonical(p.Local) == canonical(local) && p.Remotes[machine] == remoteDir {
			return p.Base[machine]
		}
	}
	return ""
}

// refuseBigFiles stops a handoff that would carry large new files as uncommitted work.
func refuseBigFiles(ctx context.Context, repo projects.Repo) error {
	big, err := projects.BigUntracked(ctx, repo, 100*1024)
	if err != nil || len(big) == 0 {
		return nil
	}
	if len(big) > 4 {
		big = append(big[:4], fmt.Sprintf("and %d more", len(big)-4))
	}
	return fmt.Errorf("new files over 100 MB would travel as uncommitted work: %s. Add them to .gitignore (and to .skyinclude if the other side needs them), then try again", strings.Join(big, ", "))
}

func (e *Engine) projectMachine(name string) (*model.Machine, error) {
	m, err := e.Machine(name)
	if err != nil {
		return nil, err
	}
	if err := e.Ready(m); err != nil {
		return nil, err
	}
	return m, nil
}

// ProjectSend puts the repo at localDir on a machine exactly as it is here: branch, commits
// (pushed or not) and uncommitted work. Nothing changes in the local repo.
func (e *Engine) ProjectSend(ctx context.Context, machine, localDir string, o HandoffOptions, r events.Reporter) (HandoffResult, error) {
	res := HandoffResult{Machine: machine}
	m, err := e.projectMachine(machine)
	if err != nil {
		return res, err
	}
	localDir = paths.Expand(localDir)
	if abs, err := filepath.Abs(localDir); err == nil {
		localDir = abs
	}
	local := projects.LocalExec()
	remote := e.remoteExec(m)

	events.Stepf(r, "Reading %s", paths.Tilde(localDir))
	if err := refuseBigFiles(ctx, projects.Repo{Dir: localDir, Run: local}); err != nil {
		return res, err
	}
	snap, err := projects.Snapshot(ctx, projects.Repo{Dir: localDir, Run: local}, false)
	if err != nil {
		return res, fmt.Errorf("%s: %w", paths.Tilde(localDir), err)
	}
	root := snap.Root
	origin := localOrigin(ctx, root)
	norm := projects.NormalizeOrigin(origin)
	name := projects.Name(origin, root)
	res.Name, res.LocalDir, res.Branch = name, root, snap.Branch
	st, _ := projects.Status(ctx, local, []string{root}, nil, false)
	if len(st) == 1 {
		what := "no uncommitted changes"
		if n := st[0].Changed + st[0].Untracked; n > 0 {
			what = plural(n, "uncommitted file")
		}
		branch := snap.Branch
		if branch == "" {
			branch = "a detached HEAD"
		}
		events.Infof(r, "%s on %s, %s", name, branch, what)
	}

	// Where it goes on the machine.
	dir := o.To
	c, _ := config.Load()
	if dir == "" && c != nil {
		for _, p := range c.Projects {
			if p.Local != "" && canonical(p.Local) == canonical(root) && p.Remotes[machine] != "" {
				dir = p.Remotes[machine]
			}
		}
	}
	if dir == "" {
		dir = "~/code/" + name
		states, rhome, err := projects.Discover(ctx, remote, nil, nil, false)
		if err != nil {
			return res, fmt.Errorf("couldn't read %s: %w", machine, err)
		}
		for _, s := range states {
			if norm != "" && projects.NormalizeOrigin(s.Origin) == norm {
				dir = tildeIn(rhome, s.Dir)
				break
			}
		}
		for _, s := range states {
			if tildeIn(rhome, s.Dir) == dir && norm != "" && s.Origin != "" && projects.NormalizeOrigin(s.Origin) != norm {
				return res, fmt.Errorf("%s on %s is a different repo (%s). Choose another folder there (--to)", dir, machine, projects.NormalizeOrigin(s.Origin))
			}
		}
	}
	if !strings.HasPrefix(dir, "/") && !strings.HasPrefix(dir, "~") {
		dir = "~/" + dir
	}
	res.RemoteDir = dir

	events.Stepf(r, "Preparing %s on %s", dir, machine)
	how, err := projects.Ensure(ctx, projects.Repo{Dir: dir, Run: remote}, projects.CloneURL(origin))
	if err != nil {
		if errors.Is(err, projects.ErrNotRepo) {
			return res, fmt.Errorf("%s on %s has files in it but isn't a git repo. Choose another folder there (--to)", dir, machine)
		}
		return res, err
	}
	switch how {
	case projects.Cloned:
		res.Created = how
		events.Infof(r, "Cloned %s there with %s's own GitHub login", norm, machine)
	case projects.Inited:
		res.Created = how
		if origin != "" {
			events.Infof(r, "%s couldn't clone %s itself, so the whole history goes from here", machine, norm)
		} else {
			events.Infof(r, "New repo there (this project has no remote)")
		}
	}

	events.Stepf(r, "Sending commits and uncommitted work")
	if err := projects.Push(ctx, root, e.transport(m, dir), snap); err != nil {
		return res, fmt.Errorf("sending to %s: %w", machine, err)
	}

	events.Stepf(r, "Switching %s to this state", machine)
	applied, err := projects.Apply(ctx, projects.Repo{Dir: dir, Run: remote}, snap.Branch, baseTree(root, machine, dir), time.Now())
	if err != nil {
		return res, err
	}
	res.Stashed, res.Backup = applied.Stashed, applied.Backup
	if applied.Stashed {
		events.Warnf(r, "%s had uncommitted work in %s; it's saved in `git stash` there (\"sky: before handoff\")", machine, dir)
	}
	if applied.Backup != "" {
		events.Warnf(r, "%s had commits this Mac doesn't; they're on the branch %s there", machine, applied.Backup)
	}
	files, err := projects.CopyIgnored(ctx, projects.Repo{Dir: root, Run: local}, projects.Repo{Dir: dir, Run: remote})
	if err != nil {
		events.Warnf(r, "Couldn't copy env files: %v", err)
	} else if len(files) > 0 {
		res.Files = files
		events.Infof(r, "Copied %s (ignored by git, needed to run)", strings.Join(files, ", "))
	}
	if err := rememberProject(name, norm, root, machine, dir, snap.Tree); err != nil {
		events.Warnf(r, "Couldn't remember the project: %v", err)
	}
	events.Donef(r, "%s", res.Summary(true))
	return res, nil
}

// ProjectBring is ProjectSend in the other direction: the repo at remoteDir on a machine
// arrives here exactly as it is there.
func (e *Engine) ProjectBring(ctx context.Context, machine, remoteDir string, o HandoffOptions, r events.Reporter) (HandoffResult, error) {
	res := HandoffResult{Machine: machine}
	m, err := e.projectMachine(machine)
	if err != nil {
		return res, err
	}
	local := projects.LocalExec()
	remote := e.remoteExec(m)
	if !strings.HasPrefix(remoteDir, "/") && !strings.HasPrefix(remoteDir, "~") {
		remoteDir = "~/" + remoteDir
	}

	events.Stepf(r, "Reading %s on %s", remoteDir, machine)
	if err := refuseBigFiles(ctx, projects.Repo{Dir: remoteDir, Run: remote}); err != nil {
		return res, err
	}
	snap, err := projects.Snapshot(ctx, projects.Repo{Dir: remoteDir, Run: remote}, true)
	if err != nil {
		return res, fmt.Errorf("%s on %s: %w", remoteDir, machine, err)
	}
	states, rhome, err := projects.Discover(ctx, remote, []string{snap.Root}, nil, false)
	if err != nil {
		return res, err
	}
	origin := ""
	for _, s := range states {
		if s.Dir == snap.Root {
			origin = s.Origin
			what := "no uncommitted changes"
			if n := s.Changed + s.Untracked; n > 0 {
				what = plural(n, "uncommitted file")
			}
			events.Infof(r, "%s on %s, %s", projects.Name(origin, snap.Root), firstNonEmpty(snap.Branch, "a detached HEAD"), what)
		}
	}
	norm := projects.NormalizeOrigin(origin)
	name := projects.Name(origin, snap.Root)
	dir := tildeIn(rhome, snap.Root)
	res.Name, res.RemoteDir, res.Branch = name, dir, snap.Branch

	// Where it goes here.
	target := o.To
	c, _ := config.Load()
	if target == "" && c != nil {
		for _, p := range c.Projects {
			if p.Remotes[machine] == dir && p.Local != "" {
				target = p.Local
			}
		}
	}
	if target == "" && c != nil {
		for _, d := range projects.FindLocal(e.projectRoots(c), 200) {
			if norm != "" && projects.NormalizeOrigin(localOrigin(ctx, d)) == norm {
				target = d
				break
			}
		}
	}
	if target == "" {
		roots := []string{}
		if c != nil {
			roots = e.projectRoots(c)
		}
		parent := filepath.Join(paths.Home(), "code")
		if len(roots) > 0 {
			parent = roots[0]
		}
		target = filepath.Join(parent, name)
	}
	target = paths.Expand(target)
	if abs, err := filepath.Abs(target); err == nil {
		target = abs
	}
	res.LocalDir = target

	events.Stepf(r, "Preparing %s", paths.Tilde(target))
	how, err := projects.Ensure(ctx, projects.Repo{Dir: target, Run: local}, origin)
	if err != nil {
		if errors.Is(err, projects.ErrNotRepo) {
			return res, fmt.Errorf("%s has files in it but isn't a git repo. Choose another folder (--to)", paths.Tilde(target))
		}
		return res, err
	}
	switch how {
	case projects.Cloned:
		res.Created = how
		events.Infof(r, "Cloned %s here first", firstNonEmpty(norm, name))
	case projects.Inited:
		res.Created = how
		events.Infof(r, "New repo here; the whole history comes from %s", machine)
	}

	events.Stepf(r, "Fetching commits and uncommitted work from %s", machine)
	if err := projects.Fetch(ctx, target, e.transport(m, dir), snap); err != nil {
		return res, fmt.Errorf("fetching from %s: %w", machine, err)
	}

	events.Stepf(r, "Switching this Mac to that state")
	applied, err := projects.Apply(ctx, projects.Repo{Dir: target, Run: local}, snap.Branch, baseTree(target, machine, dir), time.Now())
	if err != nil {
		return res, err
	}
	res.Stashed, res.Backup = applied.Stashed, applied.Backup
	if applied.Stashed {
		events.Warnf(r, "This Mac had uncommitted work in %s; it's saved in `git stash` (\"sky: before handoff\")", paths.Tilde(target))
	}
	if applied.Backup != "" {
		events.Warnf(r, "This Mac had commits %s doesn't; they're on the branch %s", machine, applied.Backup)
	}
	files, err := projects.CopyIgnored(ctx, projects.Repo{Dir: dir, Run: remote}, projects.Repo{Dir: target, Run: local})
	if err != nil {
		events.Warnf(r, "Couldn't copy env files: %v", err)
	} else if len(files) > 0 {
		res.Files = files
		events.Infof(r, "Copied %s (ignored by git, needed to run)", strings.Join(files, ", "))
	}
	if err := rememberProject(name, norm, target, machine, dir, snap.Tree); err != nil {
		events.Warnf(r, "Couldn't remember the project: %v", err)
	}
	events.Donef(r, "%s", res.Summary(false))
	return res, nil
}

// ProjectLocate finds a project's folder on a machine without scanning this computer: by
// name, or with name "" the repo that contains localDir. local is the matching folder here
// when one is known.
func (e *Engine) ProjectLocate(ctx context.Context, machine, name, localDir string) (remoteDir, local string, err error) {
	m, err := e.projectMachine(machine)
	if err != nil {
		return "", "", err
	}
	c, err := config.Load()
	if err != nil {
		return "", "", err
	}
	root, origin := "", ""
	if name == "" {
		out, err := osx.Run(ctx, "git", "-C", paths.Expand(localDir), "rev-parse", "--show-toplevel")
		if err != nil {
			return "", "", fmt.Errorf("you're not in a git repo; say which project: sky bring %s <name>", machine)
		}
		root = strings.TrimSpace(out)
		origin = projects.NormalizeOrigin(localOrigin(ctx, root))
	}
	for _, p := range c.Projects {
		d := p.Remotes[machine]
		if d == "" {
			continue
		}
		if (name != "" && strings.EqualFold(p.Name, name)) || (root != "" && p.Local != "" && canonical(p.Local) == canonical(root)) {
			return d, p.Local, nil
		}
	}
	states, rhome, err := projects.Discover(ctx, e.remoteExec(m), nil, nil, false)
	if err != nil {
		return "", "", fmt.Errorf("couldn't read %s: %w", machine, err)
	}
	var names []string
	for _, s := range states {
		n, so := projects.Name(s.Origin, s.Dir), projects.NormalizeOrigin(s.Origin)
		names = append(names, n)
		match := false
		switch {
		case name != "":
			match = strings.EqualFold(n, name) || strings.EqualFold(filepath.Base(s.Dir), name)
		case origin != "":
			match = so == origin
		default:
			match = so == "" && filepath.Base(s.Dir) == filepath.Base(root)
		}
		if match {
			return tildeIn(rhome, s.Dir), root, nil
		}
	}
	if name == "" {
		return "", "", fmt.Errorf("%s isn't on %s yet (send it first: sky send %s)", filepath.Base(root), machine, machine)
	}
	if len(names) == 0 {
		return "", "", fmt.Errorf("no project %q on %s (no repos there yet)", name, machine)
	}
	return "", "", fmt.Errorf("no project %q on %s (there: %s)", name, machine, strings.Join(names, ", "))
}

// ProjectAdd remembers a local repo so it stays at the top of the list.
func (e *Engine) ProjectAdd(ctx context.Context, dir string) (string, error) {
	dir = paths.Expand(dir)
	out, err := osx.Run(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("%s isn't a git repo", paths.Tilde(dir))
	}
	root := strings.TrimSpace(out)
	origin := localOrigin(ctx, root)
	name := projects.Name(origin, root)
	return name, rememberProject(name, projects.NormalizeOrigin(origin), root, "", "", "")
}

// ProjectForget stops remembering a project (by local folder, origin or name). With a
// machine it only forgets the copy there. No files are touched anywhere.
func (e *Engine) ProjectForget(key, machine string) error {
	defer invalidateProjects()
	return config.Update(func(c *config.Config) error {
		out := c.Projects[:0]
		found := false
		for _, p := range c.Projects {
			match := p.Name == key || (p.Origin != "" && strings.EqualFold(p.Origin, key)) || (p.Local != "" && canonical(p.Local) == canonical(paths.Expand(key)))
			if !match {
				out = append(out, p)
				continue
			}
			found = true
			if machine != "" {
				delete(p.Remotes, machine)
				delete(p.Base, machine)
				out = append(out, p)
			}
		}
		if !found {
			return fmt.Errorf("no remembered project %q", key)
		}
		c.Projects = out
		return nil
	})
}

// ProjectSetRemote changes which folder on a machine a local project maps to.
func (e *Engine) ProjectSetRemote(ctx context.Context, localDir, machine, remoteDir string) error {
	root := strings.TrimSpace(paths.Expand(localDir))
	if out, err := osx.Run(ctx, "git", "-C", root, "rev-parse", "--show-toplevel"); err == nil {
		root = strings.TrimSpace(out)
	}
	if !strings.HasPrefix(remoteDir, "/") && !strings.HasPrefix(remoteDir, "~") {
		remoteDir = "~/" + remoteDir
	}
	origin := localOrigin(ctx, root)
	if err := rememberProject(projects.Name(origin, root), projects.NormalizeOrigin(origin), root, machine, remoteDir, ""); err != nil {
		return err
	}
	return config.Update(func(c *config.Config) error { // a new folder shares nothing yet
		for _, p := range c.Projects {
			if p.Local != "" && canonical(p.Local) == canonical(root) {
				delete(p.Base, machine)
			}
		}
		return nil
	})
}

func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %ss", n, one)
}
