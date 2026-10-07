package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"skybuild/internal/paths"
)

// localScreens reads the bottom of each named session's screen (its last 30 lines that
// aren't blank) in one tmux call.
func localScreens(ctx context.Context, sessions []string) map[string]string {
	screens := map[string]string{}
	if len(sessions) == 0 {
		return screens
	}
	const mark = "@@sky-pane@@"
	var args []string
	for i, name := range sessions {
		if i > 0 {
			args = append(args, ";")
		}
		args = append(args, "display-message", "-p", "-t", "="+name+":", mark+"#{session_name}", ";", "capture-pane", "-p", "-t", "="+name+":")
	}
	out, _, _ := localTmuxRun(ctx, args...) // a session gone meanwhile leaves the rest unread
	for _, chunk := range strings.Split(out, mark)[1:] {
		name, body, _ := strings.Cut(chunk, "\n")
		var keep []string
		for _, line := range strings.Split(body, "\n") {
			if strings.TrimSpace(line) != "" {
				keep = append(keep, line)
			}
		}
		if len(keep) > 30 {
			keep = keep[len(keep)-30:]
		}
		screens[strings.TrimSpace(name)] = strings.Join(keep, "\n")
	}
	return screens
}

// branchCache keeps where each folder's HEAD file is, so a branch costs one small read.
var branchCache = struct {
	sync.Mutex
	head map[string]string // folder → its repository's HEAD file ("" when not in one)
	at   map[string]time.Time
}{head: map[string]string{}, at: map[string]time.Time{}}

// localBranch is the branch checked out in the repository a folder is in, read from git's
// own HEAD file (no git run): "" outside a repository or on a detached HEAD.
func localBranch(dir string) string {
	if dir == "" {
		return ""
	}
	branchCache.Lock()
	head, ok := branchCache.head[dir]
	if !ok || time.Since(branchCache.at[dir]) > time.Minute {
		head = findHead(dir)
		branchCache.head[dir], branchCache.at[dir] = head, time.Now()
	}
	branchCache.Unlock()
	if head == "" {
		return ""
	}
	b, err := os.ReadFile(head)
	if err != nil {
		return ""
	}
	ref, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "ref: refs/heads/")
	if !ok {
		return ""
	}
	return ref
}

// findHead looks up from dir for the repository it is in: .git is a folder, or in a
// worktree a file that says where the repository's folder is ("gitdir: …").
func findHead(dir string) string {
	home := paths.Home()
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		g := filepath.Join(d, ".git")
		if st, err := os.Stat(g); err == nil {
			if st.IsDir() {
				return filepath.Join(g, "HEAD")
			}
			if b, err := os.ReadFile(g); err == nil {
				if p, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: "); ok {
					if !filepath.IsAbs(p) {
						p = filepath.Join(d, p)
					}
					return filepath.Join(p, "HEAD")
				}
			}
			return ""
		}
		if d == home || d == filepath.Dir(d) {
			return ""
		}
	}
}
