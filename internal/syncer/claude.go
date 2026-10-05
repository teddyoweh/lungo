package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"skybuild/internal/osx"
	"skybuild/internal/secret"
)

var (
	syncDirs     = []string{"skills", "agents", "commands", "output-styles"} // mirrored per child
	syncFiles    = []string{"CLAUDE.md", "keybindings.json"}
	skipChildren = map[string]bool{"synced": true} // account-synced by Claude Code itself
)

func (x *run) claudeDir() string { return filepath.Join(x.home, ".claude") }

func localClaudeVersion(ctx context.Context) (string, error) {
	out, err := osx.Run(ctx, "claude", "--version")
	if err != nil {
		return "", fmt.Errorf("Claude Code isn't installed on this computer")
	}
	f := strings.Fields(out)
	if len(f) == 0 {
		return "", errors.New("could not read the local Claude Code version")
	}
	return f[0], nil
}

// syncVersion installs or pins the same Claude Code version as this computer.
func (x *run) syncVersion() error {
	local, err := localClaudeVersion(x.ctx)
	if err != nil {
		return err
	}
	remote := strings.Fields(x.shOK("claude --version 2>/dev/null"))
	cur := ""
	if len(remote) > 0 {
		cur = remote[0]
	}
	if cur == local {
		return nil
	}
	if cur == "" {
		x.change("Claude Code %s installed", local)
		if !x.opts.DryRun {
			_, err = x.sh("curl -fsSL https://claude.ai/install.sh | bash -s " + sq(local) + " >/dev/null 2>&1")
		}
		return err
	}
	x.change("Claude Code %s → %s", cur, local)
	if !x.opts.DryRun {
		_, err = x.sh("claude install " + sq(local) + " >/dev/null 2>&1")
	}
	return err
}

// syncClaudeFiles mirrors skills, agents, commands, output styles, CLAUDE.md and keybindings.
// Entries that exist only on the machine are left alone.
func (x *run) syncClaudeFiles() error {
	var entries []string
	for _, d := range syncDirs {
		children, err := os.ReadDir(filepath.Join(x.claudeDir(), d))
		if err != nil {
			continue
		}
		for _, c := range children {
			if !skipChildren[c.Name()] && !strings.HasPrefix(c.Name(), ".") {
				entries = append(entries, d+"/"+c.Name())
			}
		}
	}
	for _, f := range syncFiles {
		if st, err := os.Stat(filepath.Join(x.claudeDir(), f)); err == nil && !st.IsDir() {
			entries = append(entries, f)
		}
	}
	var files []file
	for _, e := range entries {
		got, err := collect(filepath.Join(x.claudeDir(), filepath.FromSlash(e)), e)
		if err != nil {
			return err
		}
		files = append(files, got...)
	}
	for i := range files {
		if isText(files[i].Data) {
			files[i].Data = []byte(x.rewrite(string(files[i].Data)))
		}
	}

	// What's on the machine under the entries we manage.
	var q []string
	for _, e := range entries {
		q = append(q, sq(e))
	}
	remote := map[string]string{}
	if len(q) > 0 {
		raw := x.shOK(shaFn + "cd ~/.claude 2>/dev/null && find " + strings.Join(q, " ") + " -type f 2>/dev/null | while IFS= read -r f; do h \"$f\"; done")
		for _, line := range strings.Split(raw, "\n") {
			if sum, name, ok := strings.Cut(line, " "); ok {
				remote[strings.TrimLeft(name, " *")] = sum
			}
		}
	}
	local := map[string]bool{}
	var changed []file
	perEntry := map[string]int{}
	for _, f := range files {
		local[f.Rel] = true
		if remote[f.Rel] != hash(f.Data) {
			changed = append(changed, f)
			perEntry[entryOf(f.Rel)]++
		}
	}
	var extra []string
	for rel := range remote {
		if !local[rel] {
			extra = append(extra, rel)
			perEntry[entryOf(rel)]++
		}
	}
	keys := make([]string, 0, len(perEntry))
	for k := range perEntry {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		x.change("%s: %d file(s) updated", k, perEntry[k])
	}
	if err := x.push("~/.claude", changed); err != nil {
		return err
	}
	if len(extra) > 0 && !x.opts.DryRun {
		var rq []string
		for _, f := range extra {
			rq = append(rq, sq(f))
		}
		if _, err := x.sh("cd ~/.claude && rm -f -- " + strings.Join(rq, " ")); err != nil {
			return err
		}
	}
	// Entries synced before but gone here now.
	now := map[string]bool{}
	for _, e := range entries {
		now[e] = true
	}
	for _, e := range x.state.Entries {
		top := strings.SplitN(e, "/", 2)[0]
		if now[e] || strings.Contains(e, "..") || !(contains(syncDirs, top) || contains(syncFiles, e)) {
			continue
		}
		x.change("%s: removed", e)
		if !x.opts.DryRun {
			x.shOK("rm -rf ~/.claude/" + sshQuoteRel(e))
		}
	}
	if !x.opts.DryRun {
		x.state.Entries = entries
	}
	return nil
}

func entryOf(rel string) string {
	parts := strings.SplitN(rel, "/", 3)
	if len(parts) >= 2 && contains(syncDirs, parts[0]) {
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}

func sshQuoteRel(rel string) string { return sq(rel) }

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// collect reads a file or directory tree (following symlinks) into files rooted at rel.
func collect(src, rel string) ([]file, error) {
	var out []file
	var walk func(src, rel string, depth int) error
	walk = func(src, rel string, depth int) error {
		if depth > 12 {
			return nil
		}
		st, err := os.Stat(src)
		if err != nil {
			return nil // dangling symlink
		}
		if !st.IsDir() {
			b, err := os.ReadFile(src)
			if err != nil {
				return nil
			}
			out = append(out, file{Rel: rel, Data: b, Mode: st.Mode()})
			return nil
		}
		children, err := os.ReadDir(src)
		if err != nil {
			return nil
		}
		for _, c := range children {
			n := c.Name()
			if n == ".DS_Store" || n == ".git" || n == "node_modules" || n == "__pycache__" {
				continue
			}
			if err := walk(filepath.Join(src, n), rel+"/"+n, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return out, walk(src, rel, 0)
}

// syncSettings writes settings.json: this computer's settings with home paths rewritten,
// auto-update off (sync pins the version), then the machine's own overrides from
// ~/.claude/settings.sky.json (and settings.mini.json, for machines set up by claude-sync-mini).
func (x *run) syncSettings() error {
	b, err := os.ReadFile(filepath.Join(x.claudeDir(), "settings.json"))
	if err != nil {
		return nil
	}
	local := map[string]any{}
	if err := json.Unmarshal([]byte(x.rewrite(string(b))), &local); err != nil {
		return fmt.Errorf("~/.claude/settings.json is not valid JSON: %w", err)
	}
	builtin := map[string]any{"env": map[string]any{"DISABLE_AUTOUPDATER": "1"}}
	remote := x.remoteJSON(".claude/settings.json")
	// Keys Claude Code writes on the machine itself (installing plugins there) are kept,
	// with this computer's values winning where both have them.
	kept := map[string]any{}
	for _, k := range []string{"extraKnownMarketplaces", "enabledPlugins"} {
		if v, ok := remote[k]; ok {
			kept[k] = v
		}
	}
	desired := deepMerge(deepMerge(deepMerge(kept, local), builtin), deepMerge(x.remoteJSON(".claude/settings.mini.json"), x.remoteJSON(".claude/settings.sky.json")))
	addSessionHooks(desired)
	if err := x.installHook(); err != nil {
		return err
	}
	if err := x.installPeek(); err != nil {
		return err
	}
	if equalJSON(remote, desired) {
		return nil
	}
	x.change("settings.json updated")
	out, _ := json.MarshalIndent(desired, "", "  ")
	return x.writeRemote(".claude/settings.json", string(out)+"\n")
}

// localMCP returns user-level MCP servers worth copying: not skipped in settings and not
// pointing at localhost (a local app can't be reached from another machine).
func (x *run) localMCP() map[string]any {
	all, _ := readJSON(filepath.Join(x.home, ".claude.json"))["mcpServers"].(map[string]any)
	out := map[string]any{}
	for name, cfg := range all {
		if contains(x.settings.SkipMCP, name) {
			continue
		}
		if c, ok := cfg.(map[string]any); ok {
			if u, _ := c["url"].(string); u != "" {
				if pu, err := url.Parse(u); err == nil {
					h := pu.Hostname()
					if h == "localhost" || h == "127.0.0.1" || h == "::1" || h == "0.0.0.0" {
						continue
					}
				}
			}
		}
		var v any
		_ = json.Unmarshal([]byte(x.rewrite(mustJSON(cfg))), &v)
		out[name] = v
	}
	return out
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (x *run) syncMCP() error {
	local := x.localMCP()
	remote, _ := x.remoteJSON(".claude.json")["mcpServers"].(map[string]any)
	synced := x.state.MCP
	for name := range synced {
		if _, keep := local[name]; keep {
			continue
		}
		if _, there := remote[name]; there {
			x.change("mcp %s: removed", name)
			if !x.opts.DryRun {
				x.shOK("claude mcp remove -s user " + sq(name))
			}
		}
	}
	names := make([]string, 0, len(local))
	for n := range local {
		names = append(names, n)
	}
	sort.Strings(names)
	next := map[string]string{}
	for _, name := range names {
		cfg := local[name]
		next[name] = digest(cfg)
		_, there := remote[name]
		if there && synced[name] == next[name] {
			continue
		}
		verb := "added"
		if there {
			verb = "updated"
		}
		x.change("mcp %s: %s", name, verb)
		if x.opts.DryRun {
			continue
		}
		if there {
			x.shOK("claude mcp remove -s user " + sq(name))
		}
		if _, err := x.sh("claude mcp add-json -s user " + sq(name) + " " + sq(mustJSON(cfg))); err != nil {
			return err
		}
	}
	if !x.opts.DryRun {
		x.state.MCP = next
	}
	return nil
}

func (x *run) syncPlugins() error {
	pdir := filepath.Join(x.claudeDir(), "plugins")
	remoteMk := x.remoteJSON(".claude/plugins/known_marketplaces.json")
	remotePl, _ := x.remoteJSON(".claude/plugins/installed_plugins.json")["plugins"].(map[string]any)
	for name, v := range readJSON(filepath.Join(pdir, "known_marketplaces.json")) {
		m, _ := v.(map[string]any)
		src, _ := m["source"].(map[string]any)
		target, _ := src["repo"].(string)
		if target == "" {
			target, _ = src["url"].(string)
		}
		if _, there := remoteMk[name]; there || target == "" {
			continue
		}
		x.change("plugin marketplace %s: added", name)
		if !x.opts.DryRun {
			if _, err := x.sh("claude plugin marketplace add " + sq(target)); err != nil {
				return err
			}
		}
	}
	installed, _ := readJSON(filepath.Join(pdir, "installed_plugins.json"))["plugins"].(map[string]any)
	for name, v := range installed {
		if _, there := remotePl[name]; there {
			continue
		}
		user := false
		list, _ := v.([]any)
		for _, i := range list {
			if im, _ := i.(map[string]any); im["scope"] == "user" {
				user = true
			}
		}
		if !user {
			continue
		}
		x.change("plugin %s: installed", name)
		if !x.opts.DryRun {
			if _, err := x.sh("claude plugin install -s user " + sq(name)); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---------- Claude login ----------

// Account is the signed-in Claude account on this computer (non-secret details).
type Account map[string]any

func (a Account) UUID() string  { s, _ := a["accountUuid"].(string); return s }
func (a Account) Email() string { s, _ := a["emailAddress"].(string); return s }

// LocalAccount reads ~/.claude.json's oauthAccount.
func LocalAccount(home string) Account {
	a, _ := readJSON(filepath.Join(home, ".claude.json"))["oauthAccount"].(map[string]any)
	if a == nil || a["accountUuid"] == nil {
		return nil
	}
	return Account(a)
}

var mintMu sync.Mutex

// Token returns the long-lived Claude token for an account, importing one made by
// claude-sync-mini if there is one, and minting a new one (browser approval) when allowed.
func Token(ctx context.Context, acct Account, mint, force bool) (string, error) {
	mintMu.Lock()
	defer mintMu.Unlock()
	key := secret.ClaudeTokenPrefix + acct.UUID()
	if !force {
		if t := secret.Get(key); t != "" {
			return t, nil
		}
		if t := importSyncMiniToken(acct.UUID()); t != "" {
			_ = secret.Set(key, t)
			return t, nil
		}
	}
	if !mint {
		return "", fmt.Errorf("no Claude token for %s yet; run `sky login claude`", acct.Email())
	}
	t, err := MintToken(ctx)
	if err != nil {
		return "", err
	}
	if err := secret.Set(key, t); err != nil {
		return "", err
	}
	return t, nil
}

// SetToken stores a token pasted by the user (from `claude setup-token`).
func SetToken(acct Account, token string) error {
	token = strings.TrimSpace(token)
	if !tokenRe.MatchString(token) {
		return errors.New("that isn't a Claude Code token (they start with sk-ant-oat01-)")
	}
	return secret.Set(secret.ClaudeTokenPrefix+acct.UUID(), token)
}

var tokenRe = regexp.MustCompile(`^sk-ant-oat01-[A-Za-z0-9_-]{40,}$`)

// importSyncMiniToken reuses the token claude-sync-mini keeps in the macOS Keychain, if it
// was minted for the same account (its comment holds the accountUuid).
func importSyncMiniToken(uuid string) string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	meta, err := osx.Run(ctx, "security", "find-generic-password", "-s", "claude-sync-mini-oauth-token")
	if err != nil || !strings.Contains(meta, `"icmt"<blob>="`+uuid+`"`) {
		return ""
	}
	t, err := osx.Run(ctx, "security", "find-generic-password", "-s", "claude-sync-mini-oauth-token", "-w")
	if err != nil {
		return ""
	}
	t = strings.TrimSpace(t)
	if !tokenRe.MatchString(t) {
		return ""
	}
	return t
}

// tokenEnvLine exports the token for every shell and wraps `claude` so each launch reads the
// file fresh: when sky switches accounts, new sessions use the new one without a re-login.
const tokenEnvLine = `[ -r "$HOME/.claude/.oauth-token" ] && export CLAUDE_CODE_OAUTH_TOKEN="$(cat "$HOME/.claude/.oauth-token")"; claude() { [ -r "$HOME/.claude/.oauth-token" ] && export CLAUDE_CODE_OAUTH_TOKEN="$(cat "$HOME/.claude/.oauth-token")"; command claude "$@"; }  # skybuild:claude-token v2`

// syncLogin signs the machine in to the same Claude account as this computer.
func (x *run) syncLogin() error {
	var token, name string
	var oauth map[string]any
	if c := x.opts.Claude; c != nil {
		token, name, oauth = c.Token, c.Name, c.OAuthAccount
	} else {
		acct := LocalAccount(x.home)
		if acct == nil {
			return errors.New("Claude Code on this computer isn't signed in")
		}
		t, err := Token(x.ctx, acct, x.opts.Interactive, x.opts.Relogin)
		if err != nil {
			return err
		}
		token, name, oauth = t, acct.Email(), map[string]any(acct)
	}
	if token == "" {
		return errors.New("no Claude token for " + name)
	}
	sum := sha256.Sum256([]byte(token + "\n"))
	want := hex.EncodeToString(sum[:])
	have := strings.Fields(x.shOK(shaFn + "h .claude/.oauth-token 2>/dev/null"))
	if len(have) == 0 || have[0] != want {
		x.change("claude login: token for %s", name)
		if err := x.writeRemote(".claude/.oauth-token", token+"\n"); err != nil {
			return err
		}
	}
	if x.state.TokenSince == nil || x.state.TokenSince[want[:16]] == 0 {
		x.state.TokenSince = map[string]float64{want[:16]: float64(time.Now().Unix())}
	}
	if err := x.ensureManagedLine(".zshenv", tokenEnvLine, "CLAUDE_CODE_OAUTH_TOKEN", false); err != nil {
		return err
	}
	if err := x.ensureManagedLine(".bashrc", tokenEnvLine, "CLAUDE_CODE_OAUTH_TOKEN", true); err != nil {
		return err
	}

	// Account details when we know them for this account, none otherwise (so a machine never
	// shows one account while using another), and no first-run wizard.
	cj := x.remoteJSON(".claude.json")
	changed := false
	if oauth != nil {
		if cur, _ := cj["oauthAccount"].(map[string]any); !equalJSON(cur, oauth) {
			cj["oauthAccount"], changed = oauth, true
		}
	} else if _, ok := cj["oauthAccount"]; ok {
		delete(cj, "oauthAccount")
		changed = true
	}
	if cj["hasCompletedOnboarding"] != true {
		cj["hasCompletedOnboarding"], changed = true, true
	}
	if theme := readJSON(filepath.Join(x.home, ".claude.json"))["theme"]; theme != nil && cj["theme"] == nil {
		cj["theme"], changed = theme, true
	}
	if changed {
		x.change("claude login: account details → %s", name)
		out, _ := json.MarshalIndent(cj, "", "  ")
		if err := x.writeRemote(".claude.json", string(out)+"\n"); err != nil {
			return err
		}
	}
	if x.opts.DryRun {
		return nil
	}
	var st struct {
		LoggedIn bool `json:"loggedIn"`
	}
	_ = json.Unmarshal([]byte(x.shOK("claude auth status 2>/dev/null")), &st)
	problem := ""
	if !st.LoggedIn {
		problem = "Claude isn't signed in on " + x.m.Name + "; run `sky login claude`"
	} else {
		for _, since := range x.state.TokenSince {
			if time.Since(time.Unix(int64(since), 0)) > 330*24*time.Hour {
				problem = "the Claude token expires within a month; run `sky login claude --new`"
			}
		}
	}
	x.state.AuthProblem = problem
	if problem != "" {
		return errors.New(problem)
	}
	return nil
}

// ensureManagedLine keeps exactly one current copy of a sky-written line in a startup file,
// replacing older sky versions of it (lines tagged "# skybuild" that mention marker).
func (x *run) ensureManagedLine(file, line, marker string, top bool) error {
	cur := x.shOK("cat " + file + " 2>/dev/null")
	var keep []string
	found := false
	for _, l := range strings.Split(strings.TrimRight(cur, "\n"), "\n") {
		if l == line {
			found = true
			keep = append(keep, l)
			continue
		}
		if strings.Contains(l, marker) && strings.Contains(l, "# skybuild") {
			continue // an older version of this line
		}
		keep = append(keep, l)
	}
	if found && len(keep) == len(strings.Split(strings.TrimRight(cur, "\n"), "\n")) {
		return nil
	}
	x.change("~/%s: loads %s", file, marker)
	if x.opts.DryRun {
		return nil
	}
	body := strings.TrimLeft(strings.Join(keep, "\n"), "\n")
	var next string
	switch {
	case found:
		next = body + "\n"
	case top:
		next = line + "\n" + body + "\n"
	default:
		next = strings.TrimRight(body, "\n") + "\n" + line + "\n"
	}
	next = strings.TrimLeft(next, "\n")
	_, err := x.shIn("cat > "+file+".sky-tmp && mv "+file+".sky-tmp "+file, next)
	return err
}

// ensureLine adds line to a remote startup file if no line containing marker is there.
// top puts it first (bash's .bashrc returns early for non-interactive shells).
func (x *run) ensureLine(file, line, marker string, top bool) error {
	cur := x.shOK("cat " + file + " 2>/dev/null")
	if strings.Contains(cur, line) {
		return nil
	}
	for _, l := range strings.Split(cur, "\n") {
		if strings.Contains(l, marker) && (strings.Contains(l, "skybuild") || strings.Contains(l, "claude-sync-mini")) {
			return nil // wired up already, maybe by an older version or claude-sync-mini
		}
	}
	x.change("~/%s: loads %s", file, marker)
	if x.opts.DryRun {
		return nil
	}
	next := cur
	if top {
		next = line + "\n" + cur
	} else {
		if cur != "" && !strings.HasSuffix(cur, "\n") {
			next += "\n"
		}
		next += line + "\n"
	}
	_, err := x.shIn("cat > "+file+".sky-tmp && mv "+file+".sky-tmp "+file, next)
	return err
}
