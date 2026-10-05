package engine

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"skybuild/internal/osx"
	"skybuild/internal/sshx"
)

// GitInfo is what a pane shows about the repository its folder is in.
type GitInfo struct {
	Repo     bool   `json:"repo"`               // the folder is inside a git work tree
	Root     string `json:"root,omitempty"`     // the repository's top folder
	Branch   string `json:"branch,omitempty"`   // branch name, or the short commit when detached
	Detached bool   `json:"detached,omitempty"` // HEAD is not on a branch
	Changed  int    `json:"changed"`            // files changed or untracked
	Ahead    int    `json:"ahead"`              // commits not on the upstream
	Behind   int    `json:"behind"`             // upstream commits not here
}

// gitInfoScript prints what GitInfo needs in one go. It takes no locks in the repository
// (GIT_OPTIONAL_LOCKS=0), so asking every few seconds never gets in the way of a git command
// the user or Claude is running there.
const gitInfoScript = `git rev-parse --is-inside-work-tree >/dev/null 2>&1 || exit 0
echo "@root $(git rev-parse --show-toplevel 2>/dev/null)"
GIT_OPTIONAL_LOCKS=0 git status --porcelain=v2 --branch 2>/dev/null | awk '/^# branch.head /{print "@head " $3} /^# branch.oid /{print "@oid " $3} /^# branch.ab /{print "@ab " $3 " " $4} !/^#/{n++} END{print "@changed " n+0}'`

func parseGitInfo(out string) GitInfo {
	var g GitInfo
	oid := ""
	for _, line := range strings.Split(out, "\n") {
		key, val, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch key {
		case "@root":
			g.Repo, g.Root = true, val
		case "@head":
			g.Branch = val
		case "@oid":
			oid = val
		case "@ab":
			a, b, _ := strings.Cut(val, " ")
			g.Ahead, _ = strconv.Atoi(strings.TrimPrefix(a, "+"))
			g.Behind, _ = strconv.Atoi(strings.TrimPrefix(b, "-"))
		case "@changed":
			g.Changed, _ = strconv.Atoi(val)
		}
	}
	if g.Branch == "(detached)" {
		g.Detached, g.Branch = true, oid
		if len(g.Branch) > 7 {
			g.Branch = g.Branch[:7]
		}
	}
	return g
}

var gitCache = struct {
	sync.Mutex
	at   map[string]time.Time
	info map[string]GitInfo
}{at: map[string]time.Time{}, info: map[string]GitInfo{}}

// localGit reports whether git can be run on this computer without side effects: on a Mac
// without the developer tools /usr/bin/git is a stub that pops up an install dialog.
var localGit = sync.OnceValue(func() bool {
	if !osx.Has("git") {
		return false
	}
	if runtime.GOOS == "darwin" && osx.Which("git") == "/usr/bin/git" {
		return exec.Command("xcode-select", "-p").Run() == nil
	}
	return true
})

// GitInfo reads the repository state of a folder on a machine, or on this computer for
// LocalMachine. A folder that isn't in a repository gives the zero value. Answers are kept
// for a few seconds, so several panes in one folder cost one look.
func (e *Engine) GitInfo(ctx context.Context, machine, dir string) (GitInfo, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return GitInfo{}, nil
	}
	key := machine + "\x00" + dir
	gitCache.Lock()
	if at, ok := gitCache.at[key]; ok && time.Since(at) < 3*time.Second {
		g := gitCache.info[key]
		gitCache.Unlock()
		return g, nil
	}
	gitCache.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var out string
	var err error
	if IsLocal(machine) {
		if !localGit() {
			return GitInfo{}, nil
		}
		var r osx.Result
		r, err = osx.Exec(ctx, osx.Cmd{Name: "/bin/sh", Args: []string{"-c", gitInfoScript}, Dir: dir})
		out = r.Stdout
	} else {
		m, merr := e.Machine(machine)
		if merr != nil {
			return GitInfo{}, merr
		}
		cd := sshx.Quote(dir)
		if strings.HasPrefix(dir, "~/") {
			cd = `"$HOME"/` + sshx.Quote(dir[2:])
		} else if dir == "~" {
			cd = `"$HOME"`
		}
		out, err = sshx.Run(ctx, e.Target(m), "cd "+cd+" 2>/dev/null || exit 0\n"+gitInfoScript)
	}
	if err != nil {
		return GitInfo{}, err
	}
	g := parseGitInfo(out)
	gitCache.Lock()
	if len(gitCache.at) > 200 { // folders come and go; don't keep them all
		gitCache.at, gitCache.info = map[string]time.Time{}, map[string]GitInfo{}
	}
	gitCache.at[key], gitCache.info[key] = time.Now(), g
	gitCache.Unlock()
	return g, nil
}
