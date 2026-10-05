package projects

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Tests run both sides of a handoff on this computer: the "machine" is another folder and the
// transport is a plain path instead of SSH. The git plumbing is identical.

func setup(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(k, "Test")
	}
	for _, k := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "test@example.com")
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRepo makes a repo with two commits on main and a .gitignore.
func newRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "-c", "init.defaultBranch=main", "init", "-q")
	write(t, filepath.Join(dir, ".gitignore"), "node_modules/\n.env*\n!.env.example\nignored.log\ndata/\nconfig/local.json\n")
	write(t, filepath.Join(dir, "README.md"), "hello\n")
	write(t, filepath.Join(dir, "src/app.js"), "console.log(1)\n")
	write(t, filepath.Join(dir, "src/old.js"), "old\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "first")
	write(t, filepath.Join(dir, "src/app.js"), "console.log(2)\n")
	git(t, dir, "commit", "-q", "-am", "second")
}

// dirty leaves uncommitted work of every kind: an edit, a staged edit, a deletion, a new
// file, a new file in a new folder, and an ignored file that must not travel.
func dirty(t *testing.T, dir string) {
	t.Helper()
	write(t, filepath.Join(dir, "README.md"), "hello, edited\n")
	write(t, filepath.Join(dir, "src/app.js"), "console.log(3)\n")
	git(t, dir, "add", "src/app.js")
	os.Remove(filepath.Join(dir, "src/old.js"))
	write(t, filepath.Join(dir, "notes.txt"), "untracked\n")
	write(t, filepath.Join(dir, "docs/new/plan.md"), "plan\n")
	write(t, filepath.Join(dir, "ignored.log"), "noise\n")
}

// fingerprint hashes every non-ignored file in the working tree (path, mode bit, content).
func fingerprint(t *testing.T, dir string) string {
	t.Helper()
	list := git(t, dir, "ls-files", "-co", "--exclude-standard")
	var names []string
	for _, n := range strings.Split(list, "\n") {
		if n != "" {
			if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
				names = append(names, n)
			}
		}
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		st, _ := os.Stat(filepath.Join(dir, n))
		h.Write([]byte(n + "\x00"))
		if st.Mode()&0o100 != 0 {
			h.Write([]byte("x"))
		}
		h.Write(b)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func repoState(t *testing.T, dir string) string {
	t.Helper()
	idx, _ := os.ReadFile(filepath.Join(dir, ".git", "index"))
	sum := sha256.Sum256(idx)
	return strings.Join([]string{
		git(t, dir, "status", "--porcelain"),
		git(t, dir, "stash", "list"),
		git(t, dir, "for-each-ref"),
		hex.EncodeToString(sum[:]),
	}, "\n--\n")
}

// send runs the whole local → destination handoff the way the engine does.
func send(t *testing.T, src, dst, origin string) (Snap, Applied, string) {
	t.Helper()
	return sendBase(t, src, dst, origin, "")
}

// sendBase is send with the tree both sides shared after the previous handoff.
func sendBase(t *testing.T, src, dst, origin, base string) (Snap, Applied, string) {
	t.Helper()
	ctx := context.Background()
	run := LocalExec()
	snap, err := Snapshot(ctx, Repo{src, run}, false)
	if err != nil {
		t.Fatal(err)
	}
	how, err := Ensure(ctx, Repo{dst, run}, origin)
	if err != nil {
		t.Fatal(err)
	}
	if err := Push(ctx, src, Transport{URL: dst}, snap); err != nil {
		t.Fatal(err)
	}
	a, err := Apply(ctx, Repo{dst, run}, snap.Branch, base, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return snap, a, how
}

func TestSnapshotLeavesSourceUntouched(t *testing.T) {
	setup(t)
	src := filepath.Join(t.TempDir(), "repo")
	newRepo(t, src)
	dirty(t, src)
	before := repoState(t, src)
	snap, err := Snapshot(context.Background(), Repo{src, LocalExec()}, false)
	if err != nil {
		t.Fatal(err)
	}
	if after := repoState(t, src); after != before {
		t.Fatalf("snapshot changed the source repo:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if snap.Branch != "main" || snap.Head != git(t, src, "rev-parse", "HEAD") {
		t.Fatalf("snap: %+v", snap)
	}
	files := git(t, src, "ls-tree", "-r", "--name-only", snap.Commit)
	for _, want := range []string{"notes.txt", "docs/new/plan.md", "README.md", "src/app.js"} {
		if !strings.Contains(files, want) {
			t.Errorf("snapshot is missing %s:\n%s", want, files)
		}
	}
	for _, not := range []string{"src/old.js", "ignored.log"} {
		if strings.Contains(files, not) {
			t.Errorf("snapshot should not contain %s", not)
		}
	}
	if got := git(t, src, "show", snap.Commit+":src/app.js"); got != "console.log(3)" {
		t.Errorf("staged edit not in snapshot: %q", got)
	}
	if entries, _ := filepath.Glob(filepath.Join(src, ".git", "sky-index*")); len(entries) != 0 {
		t.Errorf("temporary index left behind: %v", entries)
	}
}

func TestSendReproducesWorkingState(t *testing.T) {
	setup(t)
	tmp := t.TempDir()
	src, dst := filepath.Join(tmp, "laptop", "repo"), filepath.Join(tmp, "machine", "code", "repo")
	newRepo(t, src)
	git(t, src, "checkout", "-q", "-b", "feature/x")
	write(t, filepath.Join(src, "src/feature.js"), "feature\n")
	git(t, src, "add", "-A")
	git(t, src, "commit", "-q", "-m", "unpushed work")
	os.Chmod(filepath.Join(src, "src/feature.js"), 0o755)
	dirty(t, src)

	snap, applied, how := send(t, src, dst, "")
	if how != Inited || applied.Stashed || applied.Backup != "" {
		t.Fatalf("how=%s applied=%+v", how, applied)
	}
	if got := git(t, dst, "symbolic-ref", "--short", "HEAD"); got != "feature/x" {
		t.Errorf("branch on destination: %s", got)
	}
	if git(t, dst, "rev-parse", "HEAD") != snap.Head {
		t.Error("destination HEAD differs from source HEAD")
	}
	if a, b := fingerprint(t, src), fingerprint(t, dst); a != b {
		t.Errorf("working trees differ\nsrc: %s\ndst: %s", git(t, src, "status", "--porcelain"), git(t, dst, "status", "--porcelain"))
	}
	// Uncommitted work arrives uncommitted: the same paths show up, all unstaged.
	st := "\n" + git(t, dst, "status", "--porcelain") // git() trims; restore the first line's column
	if !strings.HasPrefix(st, "\n ") && !strings.HasPrefix(st, "\n?") {
		st = "\n " + st[1:]
	}
	for _, want := range []string{"\n M README.md", "\n M src/app.js", "\n M src/feature.js", "\n D src/old.js", "\n?? notes.txt", "\n?? docs/"} {
		if !strings.Contains(st, want) {
			t.Errorf("destination status missing %q:\n%s", want, st)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "ignored.log")); err == nil {
		t.Error("ignored file travelled")
	}
	// The source's snapshot and the destination's own snapshot are the same tree.
	again, err := Snapshot(context.Background(), Repo{dst, LocalExec()}, false)
	if err != nil || again.Tree != snap.Tree {
		t.Errorf("tree mismatch: %v %s vs %s", err, again.Tree, snap.Tree)
	}
}

func TestApplyStashesDirtyDestination(t *testing.T) {
	setup(t)
	tmp := t.TempDir()
	src, dst := filepath.Join(tmp, "src"), filepath.Join(tmp, "dst")
	newRepo(t, src)
	send(t, src, dst, "")
	// Someone worked on the destination without committing.
	write(t, filepath.Join(dst, "README.md"), "machine edit\n")
	write(t, filepath.Join(dst, "machine-only.txt"), "keep me\n")
	dirty(t, src)
	_, applied, how := send(t, src, dst, "")
	if how != Existed || !applied.Stashed {
		t.Fatalf("how=%s applied=%+v", how, applied)
	}
	if a, b := fingerprint(t, src), fingerprint(t, dst); a != b {
		t.Error("working trees differ after a send over a dirty destination")
	}
	stash := git(t, dst, "stash", "list")
	if !strings.Contains(stash, "sky: before handoff") {
		t.Fatalf("stash list: %q", stash)
	}
	// The stashed work is all there, including the new file.
	if got := git(t, dst, "show", "stash@{0}:README.md"); got != "machine edit" {
		t.Errorf("stashed edit: %q", got)
	}
	if got := git(t, dst, "show", "stash@{0}^3:machine-only.txt"); got != "keep me" {
		t.Errorf("stashed untracked file: %q", got)
	}
}

// Going back and forth must not fill the stash: a side that hasn't changed since the last
// handoff only holds the other side's old uncommitted work, and is simply replaced.
func TestRepeatHandoffDoesNotStashStaleCopies(t *testing.T) {
	setup(t)
	ctx := context.Background()
	run := LocalExec()
	tmp := t.TempDir()
	laptop, machine := filepath.Join(tmp, "laptop"), filepath.Join(tmp, "machine")
	newRepo(t, laptop)
	dirty(t, laptop)
	first, _, _ := send(t, laptop, machine, "")

	// The laptop keeps going and sends again: the machine still holds the first handoff.
	write(t, filepath.Join(laptop, "more.txt"), "more\n")
	os.Remove(filepath.Join(laptop, "notes.txt"))
	second, a, _ := sendBase(t, laptop, machine, "", first.Tree)
	if a.Stashed || git(t, machine, "stash", "list") != "" {
		t.Fatalf("stale copy was stashed: %+v", a)
	}
	if x, y := fingerprint(t, laptop), fingerprint(t, machine); x != y {
		t.Fatal("working trees differ after a repeat send")
	}
	if _, err := os.Stat(filepath.Join(machine, "notes.txt")); err == nil {
		t.Error("a file deleted on the laptop is still on the machine")
	}

	// Work on the machine, then bring it back: the laptop (unchanged since) isn't stashed.
	write(t, filepath.Join(machine, "machine.txt"), "machine\n")
	snap, err := Snapshot(ctx, Repo{machine, run}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := Fetch(ctx, laptop, Transport{URL: machine}, snap); err != nil {
		t.Fatal(err)
	}
	a, err = Apply(ctx, Repo{laptop, run}, snap.Branch, second.Tree, time.Now())
	if err != nil || a.Stashed || git(t, laptop, "stash", "list") != "" {
		t.Fatalf("laptop was stashed on a plain round trip: %+v %v", a, err)
	}
	if x, y := fingerprint(t, laptop), fingerprint(t, machine); x != y {
		t.Fatal("working trees differ after bringing back")
	}

	// But real new work on the destination is still protected.
	write(t, filepath.Join(machine, "unsaved.txt"), "new work on the machine\n")
	write(t, filepath.Join(laptop, "again.txt"), "again\n")
	_, a, _ = sendBase(t, laptop, machine, "", snap.Tree)
	if !a.Stashed {
		t.Fatal("new work on the destination was not stashed")
	}
	if got := git(t, machine, "show", "stash@{0}^3:unsaved.txt"); got != "new work on the machine" {
		t.Errorf("stashed file: %q", got)
	}
}

func TestApplyBacksUpCommitsItWouldDrop(t *testing.T) {
	setup(t)
	tmp := t.TempDir()
	src, dst := filepath.Join(tmp, "src"), filepath.Join(tmp, "dst")
	newRepo(t, src)
	send(t, src, dst, "")
	write(t, filepath.Join(dst, "machine.txt"), "committed on the machine\n")
	git(t, dst, "add", "-A")
	git(t, dst, "commit", "-q", "-m", "machine commit")
	lost := git(t, dst, "rev-parse", "HEAD")
	write(t, filepath.Join(src, "local.txt"), "local\n")
	git(t, src, "add", "-A")
	git(t, src, "commit", "-q", "-m", "local commit")

	_, applied, _ := send(t, src, dst, "")
	if !strings.HasPrefix(applied.Backup, "sky-backup/main-") {
		t.Fatalf("applied: %+v", applied)
	}
	if got := git(t, dst, "rev-parse", applied.Backup); got != lost {
		t.Errorf("backup branch points at %s, want %s", got, lost)
	}
	if git(t, dst, "rev-parse", "HEAD") != git(t, src, "rev-parse", "HEAD") {
		t.Error("destination did not move to the source's head")
	}
	// A fast-forward needs no backup.
	write(t, filepath.Join(src, "more.txt"), "more\n")
	git(t, src, "add", "-A")
	git(t, src, "commit", "-q", "-m", "more")
	if _, a, _ := send(t, src, dst, ""); a.Backup != "" {
		t.Errorf("unexpected backup on fast-forward: %+v", a)
	}
}

func TestBringMirrorsSend(t *testing.T) {
	setup(t)
	ctx := context.Background()
	tmp := t.TempDir()
	laptop, machine := filepath.Join(tmp, "laptop"), filepath.Join(tmp, "machine")
	newRepo(t, laptop)
	send(t, laptop, machine, "")
	// Work happens on the machine: a commit and uncommitted changes.
	write(t, filepath.Join(machine, "src/remote.js"), "remote\n")
	git(t, machine, "add", "-A")
	git(t, machine, "commit", "-q", "-m", "on the machine")
	dirty(t, machine)
	machineBefore := repoState(t, machine)

	run := LocalExec()
	snap, err := Snapshot(ctx, Repo{machine, run}, true)
	if err != nil {
		t.Fatal(err)
	}
	// Bring into a second local clone (as the live test does) with its own uncommitted work.
	second := filepath.Join(tmp, "second")
	if how, err := Ensure(ctx, Repo{second, run}, ""); err != nil || how != Inited {
		t.Fatalf("%s %v", how, err)
	}
	if err := Fetch(ctx, second, Transport{URL: machine}, snap); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(ctx, Repo{second, run}, snap.Branch, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if a, b := fingerprint(t, machine), fingerprint(t, second); a != b {
		t.Error("bring did not reproduce the machine's working tree")
	}
	if git(t, second, "rev-parse", "HEAD") != git(t, machine, "rev-parse", "HEAD") {
		t.Error("heads differ after bring")
	}
	// The machine only gained refs/sky/out/*.
	after := repoState(t, machine)
	strip := func(s string) string {
		var keep []string
		for _, l := range strings.Split(s, "\n") {
			if !strings.Contains(l, "refs/sky/out/") {
				keep = append(keep, l)
			}
		}
		return strings.Join(keep, "\n")
	}
	if strip(after) != strip(machineBefore) {
		t.Errorf("snapshot changed the machine's repo beyond refs/sky/out:\n%s\nvs\n%s", machineBefore, after)
	}
	// And back onto the original laptop repo, which has moved on with its own dirty work.
	write(t, filepath.Join(laptop, "laptop-wip.txt"), "wip\n")
	if err := Fetch(ctx, laptop, Transport{URL: machine}, snap); err != nil {
		t.Fatal(err)
	}
	a, err := Apply(ctx, Repo{laptop, run}, snap.Branch, "", time.Now())
	if err != nil || !a.Stashed {
		t.Fatalf("%+v %v", a, err)
	}
	if x, y := fingerprint(t, machine), fingerprint(t, laptop); x != y {
		t.Error("laptop does not match the machine after bring")
	}
}

func TestCloneFromOrigin(t *testing.T) {
	setup(t)
	tmp := t.TempDir()
	src, dst, origin := filepath.Join(tmp, "src"), filepath.Join(tmp, "dst"), filepath.Join(tmp, "origin.git")
	newRepo(t, src)
	git(t, tmp, "clone", "-q", "--bare", src, origin)
	git(t, src, "remote", "add", "origin", origin)
	git(t, src, "fetch", "-q", "origin")
	git(t, src, "branch", "-q", "--set-upstream-to=origin/main", "main")
	// One commit the origin doesn't have, plus uncommitted work.
	write(t, filepath.Join(src, "unpushed.txt"), "unpushed\n")
	git(t, src, "add", "-A")
	git(t, src, "commit", "-q", "-m", "unpushed")
	dirty(t, src)

	_, applied, how := send(t, src, dst, origin)
	if how != Cloned || applied.Stashed || applied.Backup != "" {
		t.Fatalf("how=%s applied=%+v", how, applied)
	}
	if got := git(t, dst, "config", "--get", "remote.origin.url"); got != origin {
		t.Errorf("origin on destination: %s", got)
	}
	if got := git(t, dst, "rev-parse", "--abbrev-ref", "main@{upstream}"); got != "origin/main" {
		t.Errorf("upstream: %s", got)
	}
	if got := git(t, dst, "rev-list", "--count", "origin/main..HEAD"); got != "1" {
		t.Errorf("unpushed commits on destination: %s", got)
	}
	if a, b := fingerprint(t, src), fingerprint(t, dst); a != b {
		t.Error("working trees differ")
	}
	// A clone that fails falls back to an empty repo with the origin recorded.
	dst2 := filepath.Join(tmp, "dst2")
	_, _, how = send(t, src, dst2, filepath.Join(tmp, "missing.git"))
	if how != Inited {
		t.Fatalf("how=%s", how)
	}
	if a, b := fingerprint(t, src), fingerprint(t, dst2); a != b {
		t.Error("working trees differ after fallback")
	}
}

func TestEnsureRefusesNonRepoFolder(t *testing.T) {
	setup(t)
	dst := filepath.Join(t.TempDir(), "stuff")
	write(t, filepath.Join(dst, "file.txt"), "not a repo\n")
	if _, err := Ensure(context.Background(), Repo{dst, LocalExec()}, ""); err != ErrNotRepo {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "file.txt")); err != nil {
		t.Error("existing folder was touched")
	}
}

func TestCopyIgnored(t *testing.T) {
	setup(t)
	tmp := t.TempDir()
	src, dst := filepath.Join(tmp, "src"), filepath.Join(tmp, "dst")
	newRepo(t, src)
	write(t, filepath.Join(src, ".env"), "SECRET=1\n")
	write(t, filepath.Join(src, ".env.local"), "LOCAL=1\n")
	write(t, filepath.Join(src, ".env.example"), "SECRET=\n") // tracked template: travels by git
	write(t, filepath.Join(src, "apps/web/.env.production"), "PROD=1\n")
	write(t, filepath.Join(src, "node_modules/pkg/.env"), "never\n")
	write(t, filepath.Join(src, "ignored.log"), "noise\n")
	write(t, filepath.Join(src, "config/local.json"), "{}\n")
	write(t, filepath.Join(src, "data/seed.csv"), "a,b\n")
	write(t, filepath.Join(src, "data/big/blob.bin"), "blob\n")
	write(t, filepath.Join(src, ".skyinclude"), "# things this repo needs to run\nconfig/local.json\ndata/\nnode_modules/pkg/.env\n")
	git(t, src, "add", "-A")
	git(t, src, "commit", "-q", "-m", "templates")
	send(t, src, dst, "")

	run := LocalExec()
	files, err := CopyIgnored(context.Background(), Repo{src, run}, Repo{dst, run})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	want := []string{".env", ".env.local", "apps/web/.env.production", "config/local.json", "data"}
	if strings.Join(files, ",") != strings.Join(want, ",") {
		t.Fatalf("copied %v, want %v", files, want)
	}
	for path, content := range map[string]string{".env": "SECRET=1\n", ".env.local": "LOCAL=1\n", "apps/web/.env.production": "PROD=1\n", "config/local.json": "{}\n", "data/seed.csv": "a,b\n", "data/big/blob.bin": "blob\n"} {
		b, err := os.ReadFile(filepath.Join(dst, path))
		if err != nil || string(b) != content {
			t.Errorf("%s: %q %v", path, b, err)
		}
	}
	for _, not := range []string{"node_modules", "ignored.log"} {
		if _, err := os.Stat(filepath.Join(dst, not)); err == nil {
			t.Errorf("%s should not be copied", not)
		}
	}
	// Nothing to copy is not an error.
	plain := filepath.Join(tmp, "plain")
	newRepo(t, plain)
	if files, err := CopyIgnored(context.Background(), Repo{plain, run}, Repo{dst, run}); err != nil || len(files) != 0 {
		t.Errorf("%v %v", files, err)
	}
}

// A file ignored only by a rule that doesn't travel (.git/info/exclude) must not look like
// new work on the destination after it is copied.
func TestCopyIgnoredLocalOnlyRule(t *testing.T) {
	setup(t)
	tmp := t.TempDir()
	src, dst := filepath.Join(tmp, "src"), filepath.Join(tmp, "dst")
	newRepo(t, src)
	write(t, filepath.Join(src, ".gitignore"), "node_modules/\n") // no .env rule in the repo
	git(t, src, "commit", "-q", "-am", "ignore less")
	write(t, filepath.Join(src, ".git/info/exclude"), ".env\nlocal data/\n")
	write(t, filepath.Join(src, ".env"), "SECRET=1\n")
	write(t, filepath.Join(src, "local data/x.bin"), "x\n")
	write(t, filepath.Join(src, ".skyinclude"), "local data/\n")
	git(t, src, "add", ".skyinclude")
	git(t, src, "commit", "-q", "-m", "include")
	snap, _, _ := send(t, src, dst, "")
	run := LocalExec()
	files, err := CopyIgnored(context.Background(), Repo{src, run}, Repo{dst, run})
	if err != nil || len(files) != 2 {
		t.Fatalf("%v %v", files, err)
	}
	if st := git(t, dst, "status", "--porcelain"); st != "" {
		t.Errorf("copied files show up as work on the destination:\n%s", st)
	}
	again, err := Snapshot(context.Background(), Repo{dst, run}, false)
	if err != nil || again.Tree != snap.Tree {
		t.Errorf("destination tree changed by copied files: %v", err)
	}
	// Copying again doesn't keep growing the exclude file with rules that already apply.
	if _, err := CopyIgnored(context.Background(), Repo{src, run}, Repo{dst, run}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dst, ".git/info/exclude"))
	if strings.Count(string(b), "/.env\n") != 1 {
		t.Errorf("exclude file:\n%s", b)
	}
}

func TestNamesWithSpaces(t *testing.T) {
	setup(t)
	tmp := t.TempDir()
	src, dst := filepath.Join(tmp, "my laptop", "my repo"), filepath.Join(tmp, "the machine", "it's code", "my repo")
	newRepo(t, src)
	write(t, filepath.Join(src, "a b/c d.txt"), "spaces\n")
	write(t, filepath.Join(src, "it's.txt"), "quote\n")
	write(t, filepath.Join(src, ".env"), "X=1\n")
	send(t, src, dst, "")
	if a, b := fingerprint(t, src), fingerprint(t, dst); a != b {
		t.Error("working trees differ")
	}
	run := LocalExec()
	files, err := CopyIgnored(context.Background(), Repo{src, run}, Repo{dst, run})
	if err != nil || len(files) != 1 {
		t.Fatalf("%v %v", files, err)
	}
	states, err := Status(context.Background(), run, []string{src, dst}, nil, true)
	if err != nil || len(states) != 2 || states[0].Tree == "" || states[0].Tree != states[1].Tree {
		t.Fatalf("%+v %v", states, err)
	}
}

func TestDetachedHead(t *testing.T) {
	setup(t)
	tmp := t.TempDir()
	src, dst := filepath.Join(tmp, "src"), filepath.Join(tmp, "dst")
	newRepo(t, src)
	git(t, src, "checkout", "-q", "--detach", "HEAD~1")
	write(t, filepath.Join(src, "wip.txt"), "wip\n")
	snap, _, _ := send(t, src, dst, "")
	if snap.Branch != "" {
		t.Fatalf("snap: %+v", snap)
	}
	if got := git(t, dst, "rev-parse", "HEAD"); got != snap.Head {
		t.Errorf("head %s", got)
	}
	if out, _ := exec.Command("git", "-C", dst, "symbolic-ref", "-q", "HEAD").Output(); len(out) != 0 {
		t.Errorf("destination should be detached, on %s", out)
	}
	if a, b := fingerprint(t, src), fingerprint(t, dst); a != b {
		t.Error("working trees differ")
	}
}

func TestBigUntracked(t *testing.T) {
	setup(t)
	dir := filepath.Join(t.TempDir(), "repo")
	newRepo(t, dir)
	blob := strings.Repeat("x", 300*1024)
	write(t, filepath.Join(dir, "dataset.bin"), blob) // new and big: reported
	write(t, filepath.Join(dir, "out put/model weights.bin"), blob)
	write(t, filepath.Join(dir, "ignored.log"), blob) // ignored
	write(t, filepath.Join(dir, "node_modules/big.bin"), blob)
	write(t, filepath.Join(dir, "tracked.bin"), blob) // tracked: part of the repo already
	git(t, dir, "add", "tracked.bin")
	git(t, dir, "commit", "-q", "-m", "tracked blob")
	write(t, filepath.Join(dir, "small.txt"), "small\n")
	big, err := BigUntracked(context.Background(), Repo{dir, LocalExec()}, 200)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(big)
	if len(big) != 2 || !strings.HasPrefix(big[0], "dataset.bin (") || !strings.HasPrefix(big[1], "out put/model weights.bin (") {
		t.Fatalf("big files: %v", big)
	}
}

func TestNoCommits(t *testing.T) {
	setup(t)
	dir := filepath.Join(t.TempDir(), "empty")
	os.MkdirAll(dir, 0o755)
	git(t, dir, "init", "-q")
	if _, err := Snapshot(context.Background(), Repo{dir, LocalExec()}, false); err != ErrNoCommits {
		t.Fatalf("err = %v", err)
	}
}

func TestStatusAndRelate(t *testing.T) {
	setup(t)
	ctx := context.Background()
	run := LocalExec()
	tmp := t.TempDir()
	local, remote := filepath.Join(tmp, "local"), filepath.Join(tmp, "remote")
	newRepo(t, local)
	state := func(dir, peer string) *State {
		t.Helper()
		var peers []string
		if peer != "" {
			peers = []string{peer}
		}
		s, err := Status(ctx, run, []string{dir}, peers, true)
		if err != nil || len(s) != 1 {
			t.Fatalf("%+v %v", s, err)
		}
		return &s[0]
	}
	relate := func(base string) Relation {
		t.Helper()
		l := state(local, "")
		r := state(remote, l.Head)
		l = state(local, r.Head)
		return Relate(l, r, base)
	}

	l := state(local, "")
	if l.Branch != "main" || l.Subject != "second" || l.Changed != 0 || l.Untracked != 0 || l.CommitAt.IsZero() || l.HasUpstream {
		t.Fatalf("state: %+v", l)
	}
	if r := Relate(l, nil, ""); r.State != OnlyLocal || r.Action() != "send" {
		t.Errorf("%+v", r)
	}

	dirty(t, local)
	snap, _, _ := send(t, local, remote, "")
	l = state(local, "")
	if l.Changed != 3 || l.Untracked != 2 { // README, app.js, old.js deleted; notes.txt, docs/
		t.Errorf("counts: %+v", l)
	}
	// Same commits, same uncommitted work on both sides: in sync.
	if r := relate(snap.Tree); r.State != Synced || r.Describe("demo") != "In sync" || r.Action() != "open" {
		t.Errorf("after send: %+v", r)
	}
	// The laptop keeps working: ahead, even though the machine still shows the old
	// uncommitted work.
	git(t, local, "add", "-A")
	git(t, local, "commit", "-q", "-m", "third")
	if r := relate(snap.Tree); r.State != LocalAhead || r.LocalCommits != 1 || r.Action() != "send" {
		t.Errorf("local ahead: %+v", r)
	}
	// Without the base tree the same situation can't be told apart from real divergence.
	if r := relate(""); r.State != Diverged {
		t.Errorf("no base: %+v", r)
	}
	// Now the machine works too: diverged.
	write(t, filepath.Join(remote, "machine.txt"), "machine\n")
	if r := relate(snap.Tree); r.State != Diverged || r.Action() != "" {
		t.Errorf("diverged: %+v", r)
	}
	// Send again; then only the machine moves: the machine is ahead.
	snap, _, _ = send(t, local, remote, "")
	git(t, remote, "commit", "-q", "--allow-empty", "-m", "machine commit")
	write(t, filepath.Join(remote, "more.txt"), "more\n")
	r := relate(snap.Tree)
	if r.State != RemoteAhead || r.RemoteCommits != 1 || !r.RemoteDirty || r.Action() != "bring" {
		t.Errorf("remote ahead: %+v", r)
	}
	if got := r.Describe("demo"); got != "demo is ahead: 1 commit and uncommitted changes" {
		t.Errorf("describe: %q", got)
	}
}

func TestDiscover(t *testing.T) {
	setup(t)
	home := t.TempDir()
	newRepo(t, filepath.Join(home, "code", "api"))
	newRepo(t, filepath.Join(home, "code", "group", "web"))
	newRepo(t, filepath.Join(home, "scratch"))
	newRepo(t, filepath.Join(home, ".hidden", "tool"))
	newRepo(t, filepath.Join(home, "code", "api", "node_modules", "dep"))
	newRepo(t, filepath.Join(home, "elsewhere", "deep", "down", "repo"))
	states, gotHome, err := Discover(context.Background(), LocalExec("HOME="+home), []string{"~/elsewhere/deep/down/repo", "~/code/api"}, nil, false)
	if err != nil || gotHome != home {
		t.Fatalf("home %q: %v", gotHome, err)
	}
	var got []string
	for _, s := range states {
		rel, _ := filepath.Rel(home, s.Dir)
		if strings.HasPrefix(rel, "..") { // macOS: /var vs /private/var
			real, _ := filepath.EvalSymlinks(home)
			rel, _ = filepath.Rel(real, s.Dir)
		}
		got = append(got, rel)
	}
	sort.Strings(got)
	want := "code/api,code/group/web,elsewhere/deep/down/repo,scratch"
	if strings.Join(got, ",") != want {
		t.Fatalf("found %v, want %s", got, want)
	}
}

func TestNormalizeOrigin(t *testing.T) {
	cases := map[string]string{
		"git@github.com:teddyoweh/skybuild.git":       "github.com/teddyoweh/skybuild",
		"https://github.com/TeddyOweh/skybuild":       "github.com/TeddyOweh/skybuild",
		"https://github.com/teddyoweh/skybuild.git/":  "github.com/teddyoweh/skybuild",
		"ssh://git@github.com:22/teddyoweh/sky.git":   "github.com/teddyoweh/sky",
		"https://token@gitlab.com/group/sub/repo.git": "gitlab.com/group/sub/repo",
		"/Users/me/repos/origin.git":                  "",
		"":                                            "",
	}
	for in, want := range cases {
		if got := NormalizeOrigin(in); got != want {
			t.Errorf("NormalizeOrigin(%q) = %q, want %q", in, got, want)
		}
	}
	if got := CloneURL("git@github.com:o/r.git"); got != "https://github.com/o/r.git" {
		t.Errorf("CloneURL: %s", got)
	}
	if got := CloneURL("git@gitlab.com:o/r.git"); got != "git@gitlab.com:o/r.git" {
		t.Errorf("CloneURL: %s", got)
	}
	if Name("git@github.com:o/my-repo.git", "/x/folder") != "my-repo" || Name("", "/x/folder/") != "folder" {
		t.Error("Name")
	}
}

func TestPickIgnored(t *testing.T) {
	got := pickIgnored(
		[]string{".env", ".env.example", "node_modules/", "apps/api/.env.local", "dist/", "secrets/", "local.db", "tmp/cache.bin", ".envrc"},
		[]string{"# comment", "secrets/", "*.db", "dist/", ""},
		[]string{"node_modules/x", "vendor/keys.json"},
	)
	sort.Strings(got)
	want := ".env,apps/api/.env.local,local.db,secrets,vendor/keys.json"
	if strings.Join(got, ",") != want {
		t.Errorf("got %v, want %s", got, want)
	}
}

func TestFindLocal(t *testing.T) {
	setup(t)
	root := t.TempDir()
	newRepo(t, filepath.Join(root, "a"))
	newRepo(t, filepath.Join(root, "group", "b"))
	newRepo(t, filepath.Join(root, "group", "sub", "too-deep"))
	newRepo(t, filepath.Join(root, ".hidden"))
	os.MkdirAll(filepath.Join(root, "not-a-repo"), 0o755)
	got := FindLocal([]string{root}, 10)
	sort.Strings(got)
	if len(got) != 2 || filepath.Base(got[0]) != "a" || filepath.Base(got[1]) != "b" {
		t.Fatalf("found %v", got)
	}
	if got := FindLocal([]string{root}, 1); len(got) != 1 {
		t.Fatalf("cap: %v", got)
	}
}
