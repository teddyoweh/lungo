package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParseGitInfo(t *testing.T) {
	g := parseGitInfo("@root /home/t/api\n@oid 0b9d7c1e5a424c8e9a512f6f0c7d1e11aabbccdd\n@head main\n@ab +2 -1\n@changed 3\n")
	if !g.Repo || g.Root != "/home/t/api" || g.Branch != "main" || g.Detached || g.Ahead != 2 || g.Behind != 1 || g.Changed != 3 {
		t.Errorf("on a branch: %+v", g)
	}
	g = parseGitInfo("@root /r\n@oid 0b9d7c1e5a424c8e9a512f6f0c7d1e11aabbccdd\n@head (detached)\n@changed 0\n")
	if !g.Detached || g.Branch != "0b9d7c1" || g.Changed != 0 {
		t.Errorf("detached: %+v", g)
	}
	if g = parseGitInfo(""); g.Repo || g.Branch != "" {
		t.Errorf("not a repository: %+v", g)
	}
}

func TestGitInfoLocal(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil || !localGit() {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	e := New()
	ctx := context.Background()
	if g, err := e.GitInfo(ctx, LocalMachine, dir); err != nil || g.Repo {
		t.Fatalf("a plain folder: %+v %v", g, err)
	}
	run("init", "-q", "-b", "trunk")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	run("add", ".")
	run("commit", "-q", "-m", "first")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("b\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "new.txt"), []byte("n\n"), 0o644)
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)
	// From a folder inside the repository, past the short cache of the first look.
	g, err := e.GitInfo(ctx, LocalMachine, sub)
	if err != nil || !g.Repo || g.Branch != "trunk" || g.Detached || g.Changed != 3 && g.Changed != 2 {
		t.Fatalf("a repository with changes: %+v %v", g, err)
	}
	if real, _ := filepath.EvalSymlinks(dir); g.Root != real && g.Root != dir {
		t.Errorf("root %q, want %q", g.Root, dir)
	}
}

func TestListFilesLocal(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"a.txt", "src/main.go", ".hidden/x", "node_modules/pkg/i.js"} {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(f)), 0o755)
		os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644)
	}
	files, err := New().ListFiles(context.Background(), LocalMachine, dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range files {
		got[f] = true
	}
	if !got["a.txt"] || !got["src/main.go"] || got[".hidden/x"] || got["node_modules/pkg/i.js"] || len(files) != 2 {
		t.Errorf("files outside a repository: %v", files)
	}
}
