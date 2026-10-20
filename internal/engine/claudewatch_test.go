package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"skybuild/internal/claudeacct"
	"skybuild/internal/config"
	"skybuild/internal/model"
	"skybuild/internal/syncer"
)

// signIn makes ~/.claude.json say Claude Code here is signed in as email, as /login does.
func signIn(t *testing.T, home, uuid, email string) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"oauthAccount": map[string]any{"accountUuid": uuid, "emailAddress": email}})
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFollowLocalLogin(t *testing.T) {
	keyring.MockInit()
	root, home := t.TempDir(), t.TempDir()
	t.Setenv("SKYBUILD_HOME", root)
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")
	ok := claudeacct.Status{State: claudeacct.StateOK, CheckedAt: time.Now()}
	limited := claudeacct.Status{State: claudeacct.StateLimited, ResetsAt: time.Now().Add(time.Hour), CheckedAt: time.Now()}
	accts := map[string]claudeacct.Status{"a": ok, "b": ok, "c": limited}
	if err := config.Update(func(c *config.Config) error {
		for _, id := range []string{"a", "b", "c"} {
			c.ClaudeAccounts = append(c.ClaudeAccounts, &model.ClaudeAccount{ID: "acct-" + id, Label: id, Email: id + "@x.io"})
		}
		c.Settings.ClaudeActive = "acct-a"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for id, st := range accts {
		if err := claudeacct.SetToken("acct-"+id, "sk-ant-oat01-"+id); err != nil {
			t.Fatal(err)
		}
		_ = claudeacct.Save("acct-"+id, st)
	}
	e := New()
	ctx := context.Background()
	follow := func() string {
		t.Helper()
		to, err := e.FollowLocalLogin(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		return to
	}
	settings := func() config.Settings { s, _ := e.Settings(); return s }
	token := func() string { b, _ := os.ReadFile(localTokenFile()); return strings.TrimSpace(string(b)) }

	// The first look only learns who is signed in.
	signIn(t, home, "u-b", "b@x.io")
	if LocalLoginChanged() != true || follow() != "" || settings().ClaudeActive != "acct-a" || LocalLoginChanged() {
		t.Fatalf("first look switched: active %s", settings().ClaudeActive)
	}
	// Rotation: /login to a usable pool account picks it, pinned.
	signIn(t, home, "u-other", "new@x.io")
	if !LocalLoginChanged() || follow() != "" {
		t.Fatal("an account not in the pool was followed")
	}
	if l := e.ClaudeLocalLogin(); l == nil || l.AccountID != "" || l.ChangedAt.IsZero() {
		t.Fatalf("a login not in the pool: %+v", l)
	}
	signIn(t, home, "u-b", "B@x.io")
	if to := follow(); to != "b" || !settings().ClaudePinned || settings().ClaudeActive != "acct-b" {
		t.Fatalf("/login to b: moved to %q, %+v", to, settings())
	}
	if LocalTmux() != "" && token() != "sk-ant-oat01-b" {
		t.Errorf("sessions here got token %q", token())
	}
	if follow() != "" {
		t.Error("followed the same login twice")
	}
	// Rotation: a /login to a limited account is left to rotation.
	signIn(t, home, "u-c", "c@x.io")
	if to := follow(); to != "" || settings().ClaudeActive != "acct-b" {
		t.Fatalf("/login to limited c moved to %q", to)
	}

	// Following this Mac's login: machines follow whatever it is, sessions here use it.
	if err := e.SetClaudeSource(ctx, "login", nil); err != nil {
		t.Fatal(err)
	}
	if s := settings(); s.ClaudeActive != "acct-c" || s.ClaudePinned || !s.ClaudeFollowLogin() {
		t.Fatalf("following the login: %+v", s)
	}
	if LocalTmux() != "" {
		if st, err := os.Stat(localTokenFile()); err != nil || st.Size() != 0 {
			t.Errorf("sessions here should use this Mac's login (an empty token file): %v", err)
		}
	}
	signIn(t, home, "u-a", "a@x.io")
	if to := follow(); to != "a" || settings().ClaudeActive != "acct-a" {
		t.Fatalf("/login to a while following: %q", to)
	}
	// Use now means rotation again.
	if err := e.UseClaudeAccount(ctx, "b", nil); err != nil {
		t.Fatal(err)
	}
	if s := settings(); s.ClaudeFollowLogin() || !s.ClaudePinned || s.ClaudeActive != "acct-b" {
		t.Fatalf("Use now: %+v", s)
	}
	if LocalTmux() != "" && token() != "sk-ant-oat01-b" {
		t.Errorf("after Use now sessions here got %q", token())
	}
}

func TestOrgBlocked(t *testing.T) {
	st := claudeacct.Status{State: claudeacct.StateInvalid, Message: "Token rejected: Your organization has disabled Claude subscription access for Claude Code · Use an Anthropic API key instead"}
	if !claudeacct.OrgBlocked(st) || !strings.Contains(claudeacct.Describe(st), "organization") {
		t.Errorf("org-blocked: %q", claudeacct.Describe(st))
	}
	if claudeacct.OrgBlocked(claudeacct.Status{State: claudeacct.StateInvalid, Message: "Token rejected: invalid"}) {
		t.Error("a bad token isn't org-blocked")
	}
}

func TestDueForCheck(t *testing.T) {
	now := time.Now()
	running := map[string]*claudeacct.Window{"seven_day": {Utilization: 0.3, ResetsAt: now.Add(48 * time.Hour)}}
	fresh := map[string]*claudeacct.Window{"seven_day": {}}
	old := now.Add(-2 * time.Hour)
	for name, c := range map[string]struct {
		st            claudeacct.Status
		active, limit bool
		want          bool
	}{
		"never checked":         {claudeacct.Status{}, false, false, true},
		"running, checked 2h":   {claudeacct.Status{State: "ok", Windows: running, CheckedAt: old}, false, false, true},
		"running, checked now":  {claudeacct.Status{State: "ok", Windows: running, CheckedAt: now}, false, false, false},
		"fresh standby":         {claudeacct.Status{State: "ok", Windows: fresh, CheckedAt: old.Add(-48 * time.Hour)}, false, false, false},
		"fresh but active":      {claudeacct.Status{State: "ok", Windows: fresh, CheckedAt: old}, true, false, true},
		"limited until later":   {claudeacct.Status{State: "limited", ResetsAt: now.Add(time.Hour), Windows: running, CheckedAt: old}, false, false, false},
		"rejected, 2h":          {claudeacct.Status{State: "invalid", CheckedAt: old}, false, false, false},
		"rejected, 7h":          {claudeacct.Status{State: "invalid", CheckedAt: now.Add(-7 * time.Hour)}, false, false, true},
		"active, limit on view": {claudeacct.Status{State: "ok", Windows: running, CheckedAt: now.Add(-3 * time.Minute)}, true, true, true},
	} {
		if got := dueForCheck(c.st, c.active, c.limit, now); got != c.want {
			t.Errorf("%s: due %v, want %v", name, got, c.want)
		}
	}
}

// fakeClaude puts a `claude` on PATH whose usage answer depends on the token it runs with:
// this computer's own login (no token) and sameToken share a week; any other token doesn't.
func fakeClaude(t *testing.T, sameToken string) {
	t.Helper()
	bin := t.TempDir()
	week := time.Now().Add(72 * time.Hour).Unix()
	script := fmt.Sprintf(`#!/bin/sh
r=%d; u=0.40
if [ -n "$CLAUDE_CODE_OAUTH_TOKEN" ] && [ "$CLAUDE_CODE_OAUTH_TOKEN" != %q ]; then r=$((r + 7200)); u=0.10; fi
echo '{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","unifiedWindows":{"seven_day":{"utilization":'$u',"resetsAt":'$r'}}}}'
echo '{"type":"result","is_error":false,"result":"OK"}'
`, week, sameToken)
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
}

// Adding after a /login: the new account takes the login's name unless its usage shows it's
// another account; with Use it becomes the active one at once.
func TestAddFromLogin(t *testing.T) {
	keyring.MockInit()
	root, home := t.TempDir(), t.TempDir()
	t.Setenv("SKYBUILD_HOME", root)
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")
	same := "sk-ant-oat01-" + strings.Repeat("s", 60)
	other := "sk-ant-oat01-" + strings.Repeat("o", 60)
	fakeClaude(t, same)
	if err := config.Update(func(c *config.Config) error {
		c.ClaudeAccounts = []*model.ClaudeAccount{{ID: "acct-a", Label: "a", Email: "a@x.io"}}
		c.Settings.ClaudeActive = "acct-a"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = claudeacct.SetToken("acct-a", "sk-ant-oat01-"+strings.Repeat("a", 60))
	e := New()
	ctx := context.Background()
	signIn(t, home, "u-new", "new@x.io")
	login := e.ClaudeLocalLogin()
	if login == nil || login.AccountID != "" {
		t.Fatalf("login: %+v", login)
	}
	want := syncer.LocalAccount(home)

	// The browser was on another account: added without the login's name, not used.
	a, err := e.AddClaudeAccount(ctx, "", other, AddOptions{Expect: want}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Email != "" || strings.Contains(a.Label, "new@x.io") {
		t.Errorf("another account took the login's name: %+v", a)
	}
	if s, _ := e.Settings(); s.ClaudeActive != "acct-a" {
		t.Errorf("used without Use: %s", s.ClaudeActive)
	}
	// The right one: named from the login and used.
	a, err = e.AddClaudeAccount(ctx, "", same, AddOptions{Expect: want, Use: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Email != "new@x.io" || a.Label != "new@x.io" {
		t.Errorf("not named from the login: %+v", a)
	}
	if s, _ := e.Settings(); s.ClaudeActive != a.ID || !s.ClaudePinned {
		t.Errorf("Use: active %s pinned %v", s.ClaudeActive, s.ClaudePinned)
	}
	if l := e.ClaudeLocalLogin(); l == nil || l.AccountID != a.ID {
		t.Errorf("the login isn't recognised as the new account: %+v", l)
	}
}

// The app adds a /login's account once, right after the /login, and not when it's in the pool.
func TestLoginToAdd(t *testing.T) {
	keyring.MockInit()
	root, home := t.TempDir(), t.TempDir()
	t.Setenv("SKYBUILD_HOME", root)
	t.Setenv("HOME", home)
	_ = config.Update(func(c *config.Config) error {
		c.ClaudeAccounts = []*model.ClaudeAccount{{ID: "acct-a", Label: "a", Email: "a@x.io"}}
		return nil
	})
	e := New()
	signIn(t, home, "u-a", "a@x.io")
	_, _ = e.FollowLocalLogin(context.Background(), nil) // first look
	if e.LoginToAdd() != nil {
		t.Error("offered without a /login")
	}
	signIn(t, home, "u-new", "new@x.io")
	_, _ = e.FollowLocalLogin(context.Background(), nil)
	if acct := e.LoginToAdd(); acct == nil || acct.Email() != "new@x.io" {
		t.Fatalf("not offered after /login: %v", acct)
	}
	if e.LoginToAdd() != nil {
		t.Error("offered twice")
	}
	signIn(t, home, "u-a", "a@x.io")
	_, _ = e.FollowLocalLogin(context.Background(), nil)
	if e.LoginToAdd() != nil {
		t.Error("offered an account the pool has")
	}
}
