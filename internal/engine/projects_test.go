package engine

import (
	"strings"
	"testing"
)

func TestProjectHelpers(t *testing.T) {
	if got := projectKey("git@github.com:Teddy/Sky.git", "/x/whatever"); got != "github.com/teddy/sky" {
		t.Errorf("key by origin: %q", got)
	}
	if got := projectKey("", "/Users/me/code/My App/"); got != "name:my app" {
		t.Errorf("key by folder: %q", got)
	}
	for origin, want := range map[string]string{"github.com/a/b": "github", "gitlab.com/a/b": "gitlab", "git.example.com/a/b": "git", "": ""} {
		if got := hostOf(origin); got != want {
			t.Errorf("hostOf(%q) = %q", origin, got)
		}
	}
	if got := tildeIn("/home/teddy", "/home/teddy/code/api"); got != "~/code/api" {
		t.Errorf("tildeIn: %q", got)
	}
	if got := tildeIn("/home/teddy", "/home/teddy2/code"); got != "/home/teddy2/code" {
		t.Errorf("tildeIn must not match a longer name: %q", got)
	}
	r := HandoffResult{Name: "api", Machine: "box", RemoteDir: "~/code/api", LocalDir: "/tmp/api", Branch: "main", Stashed: true, Backup: "sky-backup/main-1"}
	s := r.Summary(true)
	for _, want := range []string{"api is on box (~/code/api)", "on main", "git stash", "sky-backup/main-1"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary %q is missing %q", s, want)
		}
	}
	if s := (HandoffResult{Name: "api", Machine: "box", LocalDir: "/tmp/api"}).Summary(false); !strings.HasPrefix(s, "api is on this Mac (/tmp/api)") {
		t.Errorf("summary: %q", s)
	}
}
