package paths

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteFileOnlyOnChange(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if wrote, err := WriteFile(p, []byte("a"), 0o600); !wrote || err != nil {
		t.Fatalf("first write: %v %v", wrote, err)
	}
	st1, _ := os.Stat(p)
	time.Sleep(20 * time.Millisecond)
	if wrote, _ := WriteFile(p, []byte("a"), 0o600); wrote {
		t.Fatal("rewrote an unchanged file")
	}
	if st2, _ := os.Stat(p); !st2.ModTime().Equal(st1.ModTime()) {
		t.Fatal("unchanged file's time moved")
	}
	if wrote, _ := WriteFile(p, []byte("b"), 0o600); !wrote {
		t.Fatal("didn't write a change")
	}
	if b, _ := os.ReadFile(p); string(b) != "b" {
		t.Fatalf("content %q", b)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".config.json.*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestWriteFileKeepsLink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles-ssh-config")
	os.WriteFile(target, []byte("old"), 0o600)
	link := filepath.Join(dir, "config")
	os.Symlink(target, link)
	if _, err := WriteFile(link, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the link was replaced by a file")
	}
	if b, _ := os.ReadFile(target); string(b) != "new" {
		t.Fatalf("target holds %q", b)
	}
}
