package projects

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// State is where one copy of a repo stands.
type State struct {
	Dir         string    // top folder (absolute on the side it was read from)
	Origin      string    // remote.origin.url as configured
	Branch      string    // "" when detached
	Head        string    // full commit hash, "" in an empty repo
	HasUpstream bool      // the branch tracks an upstream
	Ahead       int       // commits not on the upstream
	Behind      int       // upstream commits not here
	Changed     int       // tracked files with uncommitted changes (edits, deletions, staged)
	Untracked   int       // new files not ignored
	CommitAt    time.Time // when HEAD was committed
	Subject     string    // HEAD's subject line
	Tree        string    // tree of the working tree, when asked for (equal = identical files)
	// Peers answers "how does this copy relate to that commit" for each peer head this copy
	// has: [0] commits here that the peer lacks, [1] peer commits missing here.
	Peers map[string][2]int
}

// Dirty reports uncommitted work.
func (s *State) Dirty() bool { return s != nil && s.Changed+s.Untracked > 0 }

const sep = "\x1f"

// statusLoop reads one folder per line on stdin and prints one record per repo. $peers is a
// space-separated list of commit hashes to compare against; $trees=1 also computes the
// working-tree tree (slower: it hashes changed files). It spends three git calls per repo:
// with dozens of repos, process starts are what costs.
var statusLoop = `us=$(printf '\037')
while IFS= read -r d || [ -n "$d" ]; do
  [ -n "$d" ] || continue
  case "$d" in "~") d="$HOME";; "~/"*) d="$HOME/${d#??}";; esac
  cd "$d" 2>/dev/null || continue
  if [ -e .git ]; then top=$(pwd -P); else top=$(git rev-parse --show-toplevel 2>/dev/null) || continue; cd "$top" || continue; fi
  st=$(git status --porcelain=v2 --branch 2>/dev/null) || continue
  f=$(printf '%s\n' "$st" | awk -v us="$us" '
    /^# branch.oid /  { head = $3; if (head == "(initial)") head = "" }
    /^# branch.head / { branch = $3; if (branch == "(detached)") branch = "" }
    /^# branch.ab /   { up = 1; ahead = substr($3, 2); behind = substr($4, 2) }
    /^[12u] /         { changed++ }
    /^\? /            { untracked++ }
    END { printf "%s%s%s%s%d%s%d%s%d%s%d%s%d", branch, us, head, us, up, us, ahead, us, behind, us, changed, us, untracked }')
  head=${f#*"$us"}; head=${head%%"$us"*}
  origin=$(git config --get remote.origin.url 2>/dev/null || true)
  log=""; tree=""; rel=""
  if [ -n "$head" ]; then
    log=$(git log -1 --format="%ct$us%s" 2>/dev/null | head -1)
    if [ "$trees" = 1 ]; then
` + indent(treeScript, "      ") + `    fi
    if [ -n "$peers" ]; then
      for p in $(printf '%s\n' $peers | git cat-file --batch-check 2>/dev/null | awk '$2 == "commit" { print $1 }'); do
        if [ "$p" = "$head" ]; then rel="$rel$p:0:0,"; continue; fi
        lr=$(git rev-list --left-right --count "$p...$head" 2>/dev/null) || continue
        rel="$rel$p:${lr##*[!0-9]}:${lr%%[!0-9]*},"
      done
    fi
  fi
  [ -n "$log" ] || log="$us"
  printf '%s\n' "$top$us$origin$us$f$us$log$us$tree$us$rel"
done`

func indent(s, pad string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n") + "\n"
}

var hexRe = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// statusVars sets the variables statusLoop reads.
func statusVars(peers []string, trees bool) string {
	var ok []string
	for _, p := range peers {
		if hexRe.MatchString(p) {
			ok = append(ok, p)
		}
	}
	t := "0"
	if trees {
		t = "1"
	}
	return "peers=" + q(strings.Join(ok, " ")) + "\ntrees=" + t + "\n"
}

// Status reads the state of the repos in dirs (one Exec call). peers are commit hashes to
// compare each repo against; trees also computes working-tree trees.
func Status(ctx context.Context, run Exec, dirs []string, peers []string, trees bool) ([]State, error) {
	if len(dirs) == 0 {
		return nil, nil
	}
	out, err := output(ctx, run, statusVars(peers, trees)+statusLoop, strings.NewReader(strings.Join(dirs, "\n")+"\n"))
	if err != nil {
		return nil, err
	}
	return parseStates(out), nil
}

// findRepos lists git repos two levels under the home folder (~/x, ~/code/x) and two levels
// under ~/code, skipping hidden and dependency folders, then the folders given on stdin.
const findRepos = `{
  find "$HOME" -maxdepth 3 \( -path "$HOME/.*" -o -name node_modules -o -name Library \) -prune -o -name .git -print 2>/dev/null
  if [ -d "$HOME/code" ]; then find "$HOME/code" -maxdepth 3 \( -name node_modules -o -name '.?*' ! -name .git \) -prune -o -name .git -print 2>/dev/null; fi
  cat
} | sed 's|/\.git$||' | awk 'NF && !seen[$0]++' | `

// Discover finds the repos on the far side of run (a machine) and reads their state in the
// same call. extra are folders to include even if the scan wouldn't find them. It also
// returns that side's home folder, to show paths as ~/….
func Discover(ctx context.Context, run Exec, extra []string, peers []string, trees bool) ([]State, string, error) {
	script := statusVars(peers, trees) + "printf '@@home %s\\n' \"$HOME\"\n" + findRepos + "{\n" + statusLoop + "\n}"
	out, err := output(ctx, run, script, strings.NewReader(strings.Join(extra, "\n")+"\n"))
	if err != nil {
		return nil, "", err
	}
	home := ""
	if first, _, ok := strings.Cut(out, "\n"); ok && strings.HasPrefix(first, "@@home ") {
		home = strings.TrimPrefix(first, "@@home ")
	}
	return parseStates(out), home, nil
}

func parseStates(out string) []State {
	var states []State
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		// dir, origin, branch, head, up, ahead, behind, changed, untracked, time, subject, tree, peers
		f := strings.Split(line, sep)
		if len(f) < 13 || seen[f[0]] {
			continue
		}
		seen[f[0]] = true
		s := State{Dir: f[0], Origin: f[1], Branch: f[2], Head: f[3], HasUpstream: f[4] == "1", Subject: strings.TrimSpace(strings.Join(f[10:len(f)-2], " ")), Tree: f[len(f)-2]}
		s.Ahead, _ = strconv.Atoi(f[5])
		s.Behind, _ = strconv.Atoi(f[6])
		s.Changed, _ = strconv.Atoi(f[7])
		s.Untracked, _ = strconv.Atoi(f[8])
		if n, err := strconv.ParseInt(f[9], 10, 64); err == nil && n > 0 {
			s.CommitAt = time.Unix(n, 0)
		}
		for _, rel := range strings.Split(f[len(f)-1], ",") {
			p := strings.Split(rel, ":")
			if len(p) != 3 {
				continue
			}
			a, _ := strconv.Atoi(p[1])
			b, _ := strconv.Atoi(p[2])
			if s.Peers == nil {
				s.Peers = map[string][2]int{}
			}
			s.Peers[p[0]] = [2]int{a, b}
		}
		states = append(states, s)
	}
	return states
}

// Relation states.
const (
	Synced      = "synced"       // identical files and commits
	LocalAhead  = "local-ahead"  // this computer has work the machine lacks
	RemoteAhead = "remote-ahead" // the machine has work this computer lacks
	Diverged    = "diverged"     // both have work the other lacks
	OnlyLocal   = "only-local"
	OnlyRemote  = "only-remote"
)

// Relation compares the copy here with the copy on a machine.
type Relation struct {
	State         string
	LocalCommits  int // commits here that the machine lacks (-1 = unknown)
	RemoteCommits int // commits on the machine that are missing here (-1 = unknown)
	LocalDirty    bool
	RemoteDirty   bool
}

// Relate works out the relation from both states. base is the tree both sides had after the
// last handoff ("" if unknown): a side whose files still equal it has done nothing since, so
// whatever it shows as uncommitted is the other side's old work, not new work of its own.
//
// Each side also reports how it relates to the other's head when it has that commit; between
// them that covers every case except two histories that have never met, which counts as
// diverged.
func Relate(local, remote *State, base string) Relation {
	switch {
	case local == nil && remote == nil:
		return Relation{}
	case remote == nil:
		return Relation{State: OnlyLocal, LocalDirty: local.Dirty()}
	case local == nil:
		return Relation{State: OnlyRemote, RemoteDirty: remote.Dirty()}
	}
	r := Relation{LocalDirty: local.Dirty(), RemoteDirty: remote.Dirty(), LocalCommits: -1, RemoteCommits: -1}
	switch {
	case local.Head == remote.Head:
		r.LocalCommits, r.RemoteCommits = 0, 0
	default:
		if v, ok := remote.Peers[local.Head]; ok { // the machine has our head
			r.RemoteCommits, r.LocalCommits = v[0], v[1]
		} else if v, ok := local.Peers[remote.Head]; ok { // we have the machine's head
			r.LocalCommits, r.RemoteCommits = v[0], v[1]
		}
	}
	if local.Tree != "" && local.Tree == remote.Tree && r.LocalCommits == 0 && r.RemoteCommits == 0 {
		r.State = Synced
		return r
	}
	if base != "" && local.Tree != "" && remote.Tree != "" {
		localSame, remoteSame := local.Tree == base && r.LocalCommits <= 0, remote.Tree == base && r.RemoteCommits <= 0
		switch {
		case remoteSame && !localSame: // only this computer moved since the handoff
			r.State, r.RemoteDirty = LocalAhead, false
			r.RemoteCommits = max(r.RemoteCommits, 0)
			return r
		case localSame && !remoteSame:
			r.State, r.LocalDirty = RemoteAhead, false
			r.LocalCommits = max(r.LocalCommits, 0)
			return r
		}
	}
	switch {
	case r.LocalCommits == 0 && r.RemoteCommits == 0:
		switch {
		case !r.LocalDirty && !r.RemoteDirty:
			r.State = Synced
		case r.LocalDirty && r.RemoteDirty:
			r.State = Diverged
		case r.LocalDirty:
			r.State = LocalAhead
		default:
			r.State = RemoteAhead
		}
	case r.LocalCommits < 0 || r.RemoteCommits < 0:
		r.State = Diverged
	case r.LocalCommits > 0 && r.RemoteCommits > 0:
		r.State = Diverged
	case r.LocalCommits > 0:
		r.State = LocalAhead
		if r.RemoteDirty {
			r.State = Diverged
		}
	default:
		r.State = RemoteAhead
		if r.LocalDirty {
			r.State = Diverged
		}
	}
	return r
}

func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %ss", n, one)
}

func work(commits int, dirty bool) string {
	var parts []string
	if commits > 0 {
		parts = append(parts, plural(commits, "commit"))
	}
	if dirty {
		parts = append(parts, "uncommitted changes")
	}
	return strings.Join(parts, " and ")
}

// Describe says a relation in plain words. machine is the machine's name.
func (r Relation) Describe(machine string) string {
	switch r.State {
	case Synced:
		return "In sync"
	case OnlyLocal:
		return "Only on this Mac"
	case OnlyRemote:
		return "Only on " + machine
	case LocalAhead:
		if w := work(r.LocalCommits, r.LocalDirty); w != "" {
			return "This Mac is ahead: " + w
		}
		return "This Mac is ahead"
	case RemoteAhead:
		if w := work(r.RemoteCommits, r.RemoteDirty); w != "" {
			return machine + " is ahead: " + w
		}
		return machine + " is ahead"
	case Diverged:
		if r.LocalCommits < 0 || r.RemoteCommits < 0 {
			return "Diverged: the two copies have different histories"
		}
		l, m := work(r.LocalCommits, r.LocalDirty), work(r.RemoteCommits, r.RemoteDirty)
		if l == "" || m == "" {
			return "Diverged: both copies changed since they last matched"
		}
		return "Diverged: this Mac has " + l + ", " + machine + " has " + m
	}
	return ""
}

// Action is what makes sense to do next: send, bring, open (both the same), or "" when the
// user has to choose (diverged).
func (r Relation) Action() string {
	switch r.State {
	case OnlyLocal, LocalAhead:
		return "send"
	case OnlyRemote, RemoteAhead:
		return "bring"
	case Synced:
		return "open"
	}
	return ""
}

var scpLike = regexp.MustCompile(`^(?:[^@/]+@)?([^:/]+):(.+)$`)

// NormalizeOrigin reduces a remote URL to host/owner/repo so the same repo matches whether
// it was cloned over SSH or HTTPS. "" stays "".
func NormalizeOrigin(url string) string {
	u := strings.TrimSpace(url)
	if u == "" {
		return ""
	}
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
		if at := strings.Index(u, "@"); at >= 0 && at < strings.Index(u+"/", "/") {
			u = u[at+1:]
		}
	} else if m := scpLike.FindStringSubmatch(u); m != nil && !strings.HasPrefix(u, "/") && !strings.HasPrefix(u, ".") {
		u = m[1] + "/" + m[2]
	} else {
		return "" // a local path: not something another computer can match on
	}
	u = strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git")
	host, rest, ok := strings.Cut(u, "/")
	if !ok {
		return strings.ToLower(u)
	}
	if h, _, hasPort := strings.Cut(host, ":"); hasPort {
		host = h
	}
	return strings.ToLower(host) + "/" + strings.Trim(rest, "/")
}

// CloneURL is the URL a machine should clone: GitHub SSH remotes become HTTPS, because
// machines sign in to GitHub through gh rather than an SSH key.
func CloneURL(origin string) string {
	n := NormalizeOrigin(origin)
	if strings.HasPrefix(n, "github.com/") && !strings.HasPrefix(strings.TrimSpace(origin), "https://") {
		return "https://" + n + ".git"
	}
	return strings.TrimSpace(origin)
}

// Name is the project name for a repo: the last part of its origin, else its folder name.
func Name(origin, dir string) string {
	if n := NormalizeOrigin(origin); n != "" {
		return n[strings.LastIndex(n, "/")+1:]
	}
	return filepath.Base(strings.TrimRight(dir, "/"))
}

// DefaultRoots are the folders scanned for repos on this computer when none are configured.
func DefaultRoots(home string) []string {
	var out []string
	for _, r := range []string{"Documents/codes", "code", "Code", "Developer", "Projects", "src", "dev"} {
		p := filepath.Join(home, r)
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			dup := false
			for _, o := range out { // case-insensitive filesystems: ~/code and ~/Code are one folder
				if a, err1 := os.Stat(o); err1 == nil && os.SameFile(a, st) {
					dup = true
				}
			}
			if !dup {
				out = append(out, p)
			}
		}
	}
	return out
}

// FindLocal lists git repos one or two levels under roots, most recently touched first,
// capped at max.
func FindLocal(roots []string, max int) []string {
	type hit struct {
		dir string
		at  time.Time
	}
	var hits []hit
	seen := map[string]bool{}
	check := func(dir string) bool {
		st, err := os.Stat(filepath.Join(dir, ".git"))
		if err != nil {
			return false
		}
		if !seen[dir] {
			seen[dir] = true
			at := st.ModTime()
			for _, f := range []string{"index", "HEAD", "logs/HEAD"} { // .git's own mtime rarely moves
				if fi, err := os.Stat(filepath.Join(dir, ".git", f)); err == nil && fi.ModTime().After(at) {
					at = fi.ModTime()
				}
			}
			hits = append(hits, hit{dir, at})
		}
		return true
	}
	skip := func(name string) bool { return strings.HasPrefix(name, ".") || name == "node_modules" }
	for _, root := range roots {
		if check(root) {
			continue
		}
		entries, _ := os.ReadDir(root)
		for _, e := range entries {
			if !e.IsDir() || skip(e.Name()) {
				continue
			}
			d := filepath.Join(root, e.Name())
			if check(d) {
				continue
			}
			subs, _ := os.ReadDir(d)
			for _, s := range subs {
				if s.IsDir() && !skip(s.Name()) {
					check(filepath.Join(d, s.Name()))
				}
			}
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].at.After(hits[j].at) })
	if max > 0 && len(hits) > max {
		hits = hits[:max]
	}
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.dir
	}
	return out
}
