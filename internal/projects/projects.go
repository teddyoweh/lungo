// Package projects moves a git repo's exact working state between two places: this computer
// and a machine. "Exact" means the branch, every commit (pushed or not) and the uncommitted
// work: edits, deletions and new files that aren't ignored.
//
// It is built from plain git, the same on both sides:
//
//  1. Snapshot the source: a commit of the working tree made with a temporary index
//     (git add -A; write-tree; commit-tree). The source's index, files, stash and branches
//     are not touched; only loose objects are added.
//  2. Transfer over SSH into refs/sky/* on the destination (git push when sending, git fetch
//     when bringing). Nothing the destination is using moves yet.
//  3. Apply on the destination: stash its uncommitted work if any (recoverable), keep commits
//     the incoming branch doesn't contain on a backup branch, check the branch out at the
//     incoming head, and make the working tree match the snapshot. The uncommitted work
//     arrives uncommitted (all of it unstaged).
//  4. Copy the ignored files a repo needs to run (.env files, anything in .skyinclude).
//
// Every step runs a POSIX sh script through an Exec, so the same code drives a local repo and
// a repo behind SSH, and tests can run both sides on one computer.
package projects

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"
)

// Exec runs a POSIX sh script somewhere: on this computer, or on a machine over SSH. Remote
// scripts start in the home directory.
type Exec func(ctx context.Context, script string, stdin io.Reader, stdout io.Writer) error

// Repo is a repository as seen through an Exec. Dir is a path that Exec's shell understands:
// absolute, or ~/… (home-relative).
type Repo struct {
	Dir string
	Run Exec
}

// LocalExec runs scripts with sh on this computer. env is added to the environment.
func LocalExec(env ...string) Exec {
	return func(ctx context.Context, script string, stdin io.Reader, stdout io.Writer) error {
		cmd := exec.CommandContext(ctx, "sh", "-c", script)
		cmd.Stdin, cmd.Stdout = stdin, stdout
		var errb bytes.Buffer
		cmd.Stderr = &errb
		cmd.Env = append(os.Environ(), env...)
		if err := cmd.Run(); err != nil {
			return ScriptError(err, errb.String())
		}
		return nil
	}
}

// ScriptError turns a failed script into an error that carries what it printed.
func ScriptError(err error, stderr string) error {
	msg := strings.TrimSpace(stderr)
	if len(msg) > 1200 {
		msg = "…" + msg[len(msg)-1200:]
	}
	if msg == "" {
		return err
	}
	return errors.New(msg)
}

func output(ctx context.Context, run Exec, script string, stdin io.Reader) (string, error) {
	var b bytes.Buffer
	err := run(ctx, script, stdin, &b)
	return b.String(), err
}

// q single-quotes a string for sh.
func q(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// shDir writes a directory for sh: ~/x becomes "$HOME"/'x' so it expands on the far side.
func shDir(dir string) string {
	switch {
	case dir == "~" || dir == "":
		return `"$HOME"`
	case strings.HasPrefix(dir, "~/"):
		return `"$HOME"/` + q(dir[2:])
	}
	return q(dir)
}

// Snap is a snapshot of a repo's working state.
type Snap struct {
	Root   string // the repo's top folder, as the source reports it
	Branch string // "" when HEAD is detached
	Head   string // commit HEAD points at
	Commit string // snapshot commit: Head plus everything in the working tree
	Tree   string // tree of the snapshot (equal trees = identical files)
}

// Key names the refs a snapshot travels under: the branch, or HEAD-detached.
func (s Snap) Key() string { return refKey(s.Branch) }

func refKey(branch string) string {
	if branch == "" {
		return "HEAD-detached"
	}
	return branch
}

// treeScript sets $tree to the tree of the current working tree using a throwaway index
// (the real index is only read). It expects to run in the repo's top folder.
const treeScript = `gitdir=$(git rev-parse --absolute-git-dir)
skytmp="$gitdir/sky-index.$$"
cp "$gitdir/index" "$skytmp" 2>/dev/null || true
GIT_INDEX_FILE="$skytmp" git add -A . >/dev/null 2>&1 || true
tree=$(GIT_INDEX_FILE="$skytmp" git write-tree)
rm -f "$skytmp" "$skytmp.lock"
`

// ErrNoCommits means the repo has nothing committed yet, so there is no branch to hand over.
var ErrNoCommits = errors.New("this repo has no commits yet; make a first commit, then try again")

// Snapshot captures src's working state as a commit without changing its index, files, stash
// or branches. With writeRefs it also records the result in refs/sky/out/* so the other side
// can fetch it (used on the machine when bringing work back).
func Snapshot(ctx context.Context, src Repo, writeRefs bool) (Snap, error) {
	script := "set -e\ncd " + shDir(src.Dir) + `
top=$(git rev-parse --show-toplevel 2>/dev/null) || { echo "not a git repository" >&2; exit 2; }
cd "$top"
head=$(git rev-parse -q --verify HEAD) || { echo "sky:nocommits" >&2; exit 3; }
branch=$(git symbolic-ref --short -q HEAD || true)
` + treeScript + `snap=$(GIT_AUTHOR_NAME=sky GIT_AUTHOR_EMAIL=sky@skybuild GIT_COMMITTER_NAME=sky GIT_COMMITTER_EMAIL=sky@skybuild git commit-tree "$tree" -p "$head" -m "sky: working tree snapshot")
`
	if writeRefs {
		script += `git update-ref refs/sky/out/head "$head"
git update-ref refs/sky/out/snap "$snap"
`
	}
	script += `printf '%s\n%s\n%s\n%s\n%s\n' "$top" "$branch" "$head" "$snap" "$tree"`
	out, err := output(ctx, src.Run, script, nil)
	if err != nil {
		if strings.Contains(err.Error(), "sky:nocommits") {
			return Snap{}, ErrNoCommits
		}
		return Snap{}, err
	}
	f := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(f) < 5 {
		return Snap{}, fmt.Errorf("unexpected snapshot output: %q", out)
	}
	return Snap{Root: f[0], Branch: f[1], Head: f[2], Commit: f[3], Tree: f[4]}, nil
}

// BigUntracked lists new files (not ignored, not tracked) larger than limitKB, as
// "path (size)". A snapshot would hash them into the repo and send them as uncommitted
// work, which is almost never what someone wants for a dataset or a build artifact.
func BigUntracked(ctx context.Context, src Repo, limitKB int) ([]string, error) {
	script := "cd " + shDir(src.Dir) + ` || exit 1
top=$(git rev-parse --show-toplevel 2>/dev/null) || exit 0
cd "$top" || exit 1
find . \( -name .git -o -name node_modules -o -name .venv \) -prune -o -type f -size +` + fmt.Sprint(limitKB) + `k -print 2>/dev/null | while IFS= read -r f; do
  f=${f#./}
  git check-ignore -q -- "$f" 2>/dev/null && continue
  git ls-files --error-unmatch -- "$f" >/dev/null 2>&1 && continue
  kb=$(du -k -- "$f" 2>/dev/null | cut -f1)
  printf '%s\t%s\n' "$kb" "$f"
done`
	out, err := output(ctx, src.Run, script, nil)
	if err != nil {
		return nil, err
	}
	var big []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		kb, name, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		var n float64
		fmt.Sscan(kb, &n)
		size := fmt.Sprintf("%.0f MB", n/1024)
		if n >= 1024*1024 {
			size = fmt.Sprintf("%.1f GB", n/1024/1024)
		}
		big = append(big, name+" ("+size+")")
	}
	return big, nil
}

// Ensure states.
const (
	Existed = "exists" // the repo was already there
	Cloned  = "cloned" // cloned from its origin with the destination's own credentials
	Inited  = "init"   // a new empty repo (no origin, or the clone failed)
)

// ErrNotRepo means the destination folder exists, has files, and is not a git repo.
var ErrNotRepo = errors.New("that folder exists and isn't a git repo")

// Ensure makes sure a repo exists at dst. If it doesn't, it clones origin there (so the
// destination has a proper origin and full history without it travelling from the source),
// or starts an empty repo when there is no origin or it can't be cloned.
func Ensure(ctx context.Context, dst Repo, origin string) (string, error) {
	script := "d=" + shDir(dst.Dir) + "\norigin=" + q(origin) + `
if [ -e "$d/.git" ]; then echo exists; exit 0; fi
if [ -e "$d" ] && [ -n "$(ls -A "$d" 2>/dev/null)" ]; then echo "sky:notrepo" >&2; exit 4; fi
mkdir -p "$(dirname "$d")" || exit 1
if [ -n "$origin" ]; then
  if GIT_TERMINAL_PROMPT=0 git clone -q "$origin" "$d" >/dev/null 2>&1; then echo cloned; exit 0; fi
  rm -rf "$d"
fi
git init -q "$d" || exit 1
if [ -n "$origin" ]; then git -C "$d" remote add origin "$origin"; fi
echo init`
	out, err := output(ctx, dst.Run, script, nil)
	if err != nil {
		if strings.Contains(err.Error(), "sky:notrepo") {
			return "", ErrNotRepo
		}
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Transport is how this computer's git reaches the other repo: a URL (a path, or
// user@host:path) and extra environment (GIT_SSH_COMMAND).
type Transport struct {
	URL string
	Env []string
}

func (t Transport) git(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"), t.Env...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return ScriptError(err, errb.String())
	}
	return nil
}

// Push sends a snapshot of the local repo at dir to the destination's refs/sky/head/<branch>
// and refs/sky/snap/<branch>. Hooks are skipped (--no-verify): this is a private transfer,
// not a push to a shared remote.
func Push(ctx context.Context, dir string, t Transport, s Snap) error {
	return t.git(ctx, dir, "push", "--force", "--quiet", "--no-verify", t.URL,
		s.Head+":refs/sky/head/"+s.Key(), s.Commit+":refs/sky/snap/"+s.Key())
}

// Fetch brings a snapshot recorded on the other side (Snapshot with writeRefs) into the local
// repo's refs/sky/head/<branch> and refs/sky/snap/<branch>.
func Fetch(ctx context.Context, dir string, t Transport, s Snap) error {
	return t.git(ctx, dir, "fetch", "--force", "--quiet", "--no-tags", t.URL,
		"refs/sky/out/head:refs/sky/head/"+s.Key(), "refs/sky/out/snap:refs/sky/snap/"+s.Key())
}

// Applied says what Apply had to set aside on the destination.
type Applied struct {
	Stashed bool   // uncommitted work was stashed as "sky: before handoff <time>"
	Backup  string // branch holding commits the incoming branch didn't contain ("" = none)
}

// Apply makes the destination's working state match a transferred snapshot. Anything it
// would overwrite is kept: uncommitted work goes to the stash, and commits the incoming
// branch doesn't contain stay on a sky-backup/… branch.
//
// base is the tree both sides had after the previous handoff ("" if there wasn't one). A
// destination whose files still equal it has done no work of its own (what looks
// uncommitted there is the previous handoff's copy), so that is replaced without a stash.
func Apply(ctx context.Context, dst Repo, branch, base string, when time.Time) (Applied, error) {
	stamp := when.Format("2006-01-02 15:04")
	if !hexRe.MatchString(base) {
		base = ""
	}
	script := "set -e\ncd " + shDir(dst.Dir) + "\nbranch=" + q(branch) + "\nkey=" + q(refKey(branch)) + "\nbase=" + q(base) +
		"\nstamp=" + q(stamp) + "\nts=" + q(when.Format("20060102-150405")) + `
git rev-parse -q --verify "refs/sky/head/$key" >/dev/null || { echo "the transfer did not arrive (refs/sky/head/$key is missing)" >&2; exit 5; }
stashed=0
backup=""
if git rev-parse -q --verify HEAD >/dev/null 2>&1; then
  if [ -n "$(git status --porcelain)" ]; then
    tree=""
    if [ -n "$base" ]; then
` + indent(treeScript, "      ") + `    fi
    if [ -n "$base" ] && [ "$tree" = "$base" ]; then
      git reset -q --hard
      git clean -fdq
    else
      git stash push -u -q -m "sky: before handoff $stamp"
      stashed=1
    fi
  fi
  if [ -n "$branch" ] && cur=$(git rev-parse -q --verify "refs/heads/$branch"); then
    if ! git merge-base --is-ancestor "$cur" "refs/sky/head/$key"; then
      backup="sky-backup/$branch-$ts"
      git branch -q "$backup" "$cur"
    fi
  fi
fi
if [ -n "$branch" ]; then
  git checkout -q -B "$branch" "refs/sky/head/$key"
  if git rev-parse -q --verify "refs/remotes/origin/$branch" >/dev/null; then
    git branch -q --set-upstream-to="origin/$branch" "$branch" >/dev/null 2>&1 || true
  fi
else
  git checkout -q --detach "refs/sky/head/$key"
fi
if [ "$(git rev-parse "refs/sky/snap/$key^{tree}")" != "$(git rev-parse 'HEAD^{tree}')" ]; then
  git restore --source="refs/sky/snap/$key" --worktree -- .
fi
printf 'stashed=%s\nbackup=%s\n' "$stashed" "$backup"`
	out, err := output(ctx, dst.Run, script, nil)
	if err != nil {
		return Applied{}, fmt.Errorf("%w\nNothing is lost: the incoming work is in refs/sky/snap/%s, and any work that was in the folder is in `git stash list` (\"sky: before handoff %s\")", err, refKey(branch), stamp)
	}
	var a Applied
	for _, line := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "stashed":
			a.Stashed = v == "1"
		case "backup":
			a.Backup = v
		}
	}
	return a, nil
}

// Folders that are rebuilt on the other side and never copied, even when asked.
var neverCopy = map[string]bool{
	"node_modules": true, ".venv": true, "venv": true, "__pycache__": true, ".next": true, ".turbo": true,
	"dist": true, "build": true, "target": true, ".cache": true, ".git": true, ".tox": true,
	".mypy_cache": true, ".pytest_cache": true, ".gradle": true, "Pods": true,
}

var envTemplates = map[string]bool{".env.example": true, ".env.sample": true, ".env.template": true, ".env.dist": true, ".env.defaults": true}

func isEnvFile(base string) bool {
	return (base == ".env" || strings.HasPrefix(base, ".env.")) && !envTemplates[base]
}

func forbidden(rel string) bool {
	for _, part := range strings.Split(strings.Trim(rel, "/"), "/") {
		if neverCopy[part] {
			return true
		}
	}
	return false
}

// pickIgnored chooses which ignored paths travel with a repo: env files, plus whatever
// .skyinclude asks for. ignored is `git ls-files --others --ignored --directory` output
// (ignored folders appear once, with a trailing slash); include is .skyinclude's lines;
// exists lists the .skyinclude lines that name something that exists.
func pickIgnored(ignored, include, exists []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = strings.TrimSuffix(p, "/")
		if p == "" || seen[p] || forbidden(p) || strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	var patterns []string
	for _, line := range include {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			patterns = append(patterns, strings.TrimPrefix(line, "./"))
		}
	}
	for _, rel := range ignored {
		if rel == "" {
			continue
		}
		clean := strings.TrimSuffix(rel, "/")
		if !strings.HasSuffix(rel, "/") && isEnvFile(path.Base(clean)) {
			add(clean)
			continue
		}
		for _, p := range patterns {
			pc := strings.TrimSuffix(p, "/")
			okFull, _ := path.Match(pc, clean)
			okBase, _ := path.Match(pc, path.Base(clean))
			if okFull || okBase || clean == pc || strings.HasPrefix(clean, pc+"/") {
				add(clean)
				break
			}
		}
	}
	for _, p := range exists { // named outright: travels even from inside an ignored folder
		add(strings.TrimPrefix(strings.TrimSpace(p), "./"))
	}
	return out
}

// CopyIgnored copies the ignored files a repo needs to run from src to dst: .env and .env.*
// (not the .example/.sample templates) and anything listed in .skyinclude. It returns the
// paths it copied.
func CopyIgnored(ctx context.Context, src, dst Repo) ([]string, error) {
	list := "cd " + shDir(src.Dir) + ` || exit 1
git ls-files --others --ignored --exclude-standard --directory 2>/dev/null
echo "@@skyinclude@@"
cat .skyinclude 2>/dev/null
echo
echo "@@skyexists@@"
if [ -f .skyinclude ]; then
  while IFS= read -r p || [ -n "$p" ]; do
    case "$p" in ""|"#"*|*"*"*|*"?"*|*"["*) continue;; esac
    if [ -e "$p" ]; then printf '%s\n' "$p"; fi
  done < .skyinclude
fi`
	out, err := output(ctx, src.Run, list, nil)
	if err != nil {
		return nil, err
	}
	ignoredPart, rest, _ := strings.Cut(out, "@@skyinclude@@\n")
	includePart, existsPart, _ := strings.Cut(rest, "@@skyexists@@\n")
	files := pickIgnored(strings.Split(ignoredPart, "\n"), strings.Split(includePart, "\n"), strings.Split(strings.TrimSpace(existsPart), "\n"))
	if len(files) == 0 {
		return nil, nil
	}
	var quoted []string
	for _, f := range files {
		quoted = append(quoted, q(f))
	}
	pr, pw := io.Pipe()
	errc := make(chan error, 1)
	go func() {
		err := src.Run(ctx, "cd "+shDir(src.Dir)+" && COPYFILE_DISABLE=1 tar --no-xattrs -cf - -- "+strings.Join(quoted, " "), nil, pw)
		pw.CloseWithError(err)
		errc <- err
	}()
	// After unpacking, anything the destination's own rules don't ignore (the source ignored it
	// through a rule that doesn't travel: .git/info/exclude or a global ignore file) is added
	// to the destination's .git/info/exclude, so it doesn't show up as new work there.
	extract := "cd " + shDir(dst.Dir) + ` || exit 1
if tar --version 2>/dev/null | grep -q GNU; then tar --warning=no-unknown-keyword -xf -; else tar -xf -; fi || exit 1
ex="$(git rev-parse --git-dir)/info/exclude"
for p in ` + strings.Join(quoted, " ") + `; do
  if ! git check-ignore -q -- "$p" 2>/dev/null; then
    mkdir -p "$(dirname "$ex")"
    if [ -d "$p" ]; then printf '/%s/\n' "$p" >> "$ex"; else printf '/%s\n' "$p" >> "$ex"; fi
  fi
done`
	err = dst.Run(ctx, extract, pr, io.Discard)
	pr.CloseWithError(err)
	if serr := <-errc; serr != nil && err == nil {
		err = serr
	}
	if err != nil {
		return nil, fmt.Errorf("copying %s: %w", strings.Join(files, ", "), err)
	}
	return files, nil
}
