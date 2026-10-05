package syncer

import (
	"strings"
	"testing"

	"skybuild/internal/model"
)

func TestGitConfigForLinux(t *testing.T) {
	x := &run{home: "/Users/teddy", remoteHome: "/home/teddy", remoteOS: "linux", m: &model.Machine{Sync: DefaultItems()}}
	in := `[user]
	name = Teddy
[credential "https://github.com"]
	helper =
	helper = !/opt/homebrew/bin/gh auth git-credential
[credential]
	helper = osxkeychain
	helper = store
[core]
	excludesfile = /Users/teddy/.gitignore_global
`
	out := x.gitConfigFor(in)
	if strings.Contains(out, "osxkeychain") {
		t.Error("osxkeychain helper kept on linux")
	}
	if !strings.Contains(out, "helper = !gh auth git-credential") || strings.Contains(out, "/opt/homebrew") {
		t.Errorf("gh helper not rewritten:\n%s", out)
	}
	if !strings.Contains(out, "/home/teddy/.gitignore_global") {
		t.Error("home path not rewritten")
	}
	if strings.Count(out, "gh auth git-credential") != 1 {
		t.Errorf("gh block added twice:\n%s", out)
	}
}

func TestGitConfigAddsGhHelper(t *testing.T) {
	x := &run{home: "/Users/t", remoteHome: "/home/t", remoteOS: "linux", m: &model.Machine{Sync: DefaultItems()}}
	out := x.gitConfigFor("[user]\n\tname = T\n")
	if !strings.Contains(out, `[credential "https://github.com"]`) {
		t.Errorf("expected gh helper block:\n%s", out)
	}
	x.m.Sync = []string{ItemGit}
	if strings.Contains(x.gitConfigFor("[user]\n"), "gh auth") {
		t.Error("gh helper added without the github item")
	}
}

func TestAddSessionHooksKeepsUserHooks(t *testing.T) {
	s := map[string]any{"hooks": map[string]any{
		"Stop": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "say done"}}}},
	}}
	addSessionHooks(s)
	addSessionHooks(s) // idempotent
	stop := s["hooks"].(map[string]any)["Stop"].([]any)
	if len(stop) != 2 {
		t.Fatalf("Stop hooks: %v", stop)
	}
	if !strings.Contains(mustJSON(stop), "say done") || !strings.Contains(mustJSON(stop), "sky-session-hook") {
		t.Errorf("hooks: %s", mustJSON(stop))
	}
	for _, ev := range []string{"UserPromptSubmit", "Notification", "PostToolUse"} {
		if _, ok := s["hooks"].(map[string]any)[ev]; !ok {
			t.Errorf("missing %s", ev)
		}
	}
}

func TestDeepMerge(t *testing.T) {
	a := map[string]any{"env": map[string]any{"A": "1", "B": "2"}, "model": "x"}
	b := map[string]any{"env": map[string]any{"B": "3"}, "theme": "dark"}
	m := deepMerge(a, b)
	env := m["env"].(map[string]any)
	if env["A"] != "1" || env["B"] != "3" || m["model"] != "x" || m["theme"] != "dark" {
		t.Errorf("merge: %v", m)
	}
}

func TestSecretExport(t *testing.T) {
	cases := map[string]string{
		`export OPENAI_API_KEY="sk-1"`:       "OPENAI_API_KEY",
		`  export GH_TOKEN=$(gh auth token)`: "GH_TOKEN",
		`export PATH=/usr/bin`:               "",
		`# export FOO_SECRET=1`:              "",
	}
	for line, want := range cases {
		m := secretExport.FindStringSubmatch(line)
		got := ""
		if m != nil {
			got = m[1]
		}
		if got != want {
			t.Errorf("%q → %q, want %q", line, got, want)
		}
	}
}

func TestEntryOf(t *testing.T) {
	if entryOf("skills/foo/bar/x.md") != "skills/foo" || entryOf("CLAUDE.md") != "CLAUDE.md" {
		t.Error("entryOf")
	}
}
