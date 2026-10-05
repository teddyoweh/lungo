// Package syncer pushes this computer's developer setup to machines: Claude Code (version,
// settings, skills, MCP servers, plugins and login), GitHub, git config, API keys and CLI
// credential files. This computer is the source of truth; nothing flows back.
//
// It is a generalised port of claude-sync-mini: same rules, any number of machines, Linux
// or macOS on the other end.
package syncer

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gofrs/flock"

	"skybuild/internal/config"
	"skybuild/internal/events"
	"skybuild/internal/model"
	"skybuild/internal/paths"
	"skybuild/internal/sshx"
)

// Item is one thing that can be synced, toggled per machine.
type Item struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// Item IDs.
const (
	ItemClaude      = "claude"
	ItemClaudeLogin = "claude-login"
	ItemGitHub      = "github"
	ItemGit         = "git"
	ItemEnv         = "env"
	ItemCredentials = "credentials"
	ItemFiles       = "files"
)

var items = []Item{
	{ItemClaude, "Claude Code", "Version, settings, skills, agents, commands, CLAUDE.md, MCP servers and plugins"},
	{ItemClaudeLogin, "Claude login", "Signs the machine in to your Claude account with a long-lived token"},
	{ItemGitHub, "GitHub", "Every gh account, the active one, and git push over HTTPS"},
	{ItemGit, "Git config", "~/.gitconfig, with Mac-only credential helpers swapped for gh"},
	{ItemEnv, "API keys", "Secret exports from your shell startup files (…_KEY, …_TOKEN, …_SECRET)"},
	{ItemCredentials, "CLI logins", "gcloud, AWS, Docker, npm, Modal, Fly, Railway, Vercel, Stripe, kube and more"},
	{ItemFiles, "Extra files", "Files and folders under ~ you add in settings"},
}

// Items lists everything that can be synced.
func Items() []Item { return items }

// DefaultItems is every item: new machines get the full setup.
func DefaultItems() []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

// Options change one sync run.
type Options struct {
	Only        []string `json:"only,omitempty"`   // limit to these items
	DryRun      bool     `json:"dryRun,omitempty"` // report what would change
	Relogin     bool     `json:"relogin,omitempty"`
	Interactive bool     `json:"interactive,omitempty"` // allowed to open a browser (minting a Claude token)
	// Claude is the account machines sign in with, picked by the engine from the account
	// pool. Nil means "the account this computer is signed in to".
	Claude *ClaudeLogin `json:"-"`
	// APIKeys are keys stored in sky (not in shell files), with the machines they go to.
	APIKeys []ManagedKey `json:"-"`
}

// ManagedKey is an API key stored in sky.
type ManagedKey struct {
	Name     string
	Value    string
	Machines []string // empty = every machine
}

// ClaudeLogin is one account's token and display details, for the claude-login item.
type ClaudeLogin struct {
	ID           string
	Name         string
	Token        string
	OAuthAccount map[string]any // nil when only the token is known
}

// Result of syncing one machine.
type Result struct {
	Machine string    `json:"machine"`
	Changes []string  `json:"changes"`
	Errors  []string  `json:"errors"`
	Skipped string    `json:"skipped,omitempty"` // why nothing ran (unreachable, stopped)
	At      time.Time `json:"at"`
}

// State is what sync remembers per machine between runs.
type State struct {
	Entries      []string           `json:"entries,omitempty"`
	MCP          map[string]string  `json:"mcp,omitempty"`
	TokenSince   map[string]float64 `json:"tokenSince,omitempty"`
	AuthProblem  string             `json:"authProblem,omitempty"`
	TokensBackup string             `json:"tokensBackup,omitempty"`
	GitBackup    bool               `json:"gitBackup,omitempty"`
	Last         *Result            `json:"last,omitempty"`
}

func statePath(name string) string { return filepath.Join(paths.State(), name+".json") }

// LoadState reads a machine's sync state (empty if none).
func LoadState(name string) *State {
	s := &State{}
	if b, err := os.ReadFile(statePath(name)); err == nil {
		_ = json.Unmarshal(b, s)
	}
	return s
}

func (s *State) save(name string) {
	b, _ := json.MarshalIndent(s, "", "  ")
	_ = os.WriteFile(statePath(name), b, 0o600)
}

// Forget deletes a machine's sync state.
func Forget(name string) { os.Remove(statePath(name)) }

// machine locks stop two syncs of the same machine overlapping (agent + manual).
var locks sync.Map

type run struct {
	ctx        context.Context
	m          *model.Machine
	t          sshx.Target
	settings   config.Settings
	opts       Options
	r          events.Reporter
	state      *State
	home       string // local home
	remoteHome string
	remoteOS   string // linux | darwin
	res        *Result
}

// Run syncs one machine.
func Run(ctx context.Context, m *model.Machine, t sshx.Target, settings config.Settings, opts Options, r events.Reporter) Result {
	res := Result{Machine: m.Name, At: time.Now(), Changes: []string{}, Errors: []string{}}
	mu, _ := locks.LoadOrStore(m.Name, &sync.Mutex{})
	if !mu.(*sync.Mutex).TryLock() {
		res.Skipped = "a sync is already running"
		return res
	}
	defer mu.(*sync.Mutex).Unlock()

	if m.Status == model.StatusStopped || m.Status == model.StatusMissing {
		res.Skipped = "machine is " + m.Status
		return res
	}
	if err := paths.Ensure(); err != nil {
		res.Errors = append(res.Errors, err.Error())
		return res
	}
	// Across processes too: the background agent, the CLI and the desktop app.
	fl := flock.New(filepath.Join(paths.State(), m.Name+".lock"))
	if ok, _ := fl.TryLock(); !ok {
		res.Skipped = "a sync is already running"
		return res
	}
	defer fl.Unlock()
	x := &run{ctx: ctx, m: m, t: t, settings: settings, opts: opts, r: r, home: paths.Home(), res: &res,
		state: LoadState(m.Name)}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	out, err := sshx.Run(cctx, t, `echo "$HOME"; uname -s`)
	cancel()
	if err != nil {
		res.Skipped = "unreachable"
		return res
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		res.Skipped = "unexpected reply from machine"
		return res
	}
	x.remoteHome, x.remoteOS = strings.TrimSpace(lines[0]), strings.ToLower(strings.TrimSpace(lines[1]))

	enabled := map[string]bool{}
	for _, id := range m.Sync {
		enabled[id] = true
	}
	if len(opts.Only) > 0 {
		only := map[string]bool{}
		for _, id := range opts.Only {
			only[id] = true
		}
		for id := range enabled {
			if !only[id] {
				delete(enabled, id)
			}
		}
	}
	steps := []struct {
		item string
		name string
		fn   func() error
	}{
		{ItemClaude, "claude version", x.syncVersion},
		{ItemClaude, "claude files", x.syncClaudeFiles},
		{ItemClaude, "claude settings", x.syncSettings},
		{ItemClaude, "mcp servers", x.syncMCP},
		{ItemClaude, "plugins", x.syncPlugins},
		{ItemClaude, "tmux settings", x.syncTmux},
		{ItemClaudeLogin, "claude login", x.syncLogin},
		{ItemGit, "git config", x.syncGitConfig},
		{ItemGitHub, "github", x.syncGitHub},
		{ItemEnv, "api keys", x.syncEnv},
		{ItemCredentials, "cli logins", func() error { return x.syncHomeFiles(credentialPaths(x)) }},
		{ItemFiles, "extra files", func() error { return x.syncHomeFiles(extraPaths(x)) }},
	}
	for _, s := range steps {
		if !enabled[s.item] {
			continue
		}
		if err := s.fn(); err != nil {
			x.fail("%s: %v", s.name, err)
		}
	}
	if len(opts.Only) == 0 { // a sync limited to some items stays limited to them
		if err := x.syncIdle(); err != nil {
			x.fail("stop when idle: %v", err)
		}
	}
	if !opts.DryRun {
		x.state.Last = &res
		x.state.save(m.Name)
	}
	return res
}

func (x *run) change(f string, a ...any) {
	msg := fmt.Sprintf(f, a...)
	if x.opts.DryRun {
		msg = "would: " + msg
	}
	x.res.Changes = append(x.res.Changes, msg)
	events.Infof(x.r, "%s", msg)
}

func (x *run) fail(f string, a ...any) {
	msg := fmt.Sprintf(f, a...)
	x.res.Errors = append(x.res.Errors, msg)
	events.Warnf(x.r, "%s", msg)
}

// sh runs a command on the machine (from its home directory).
func (x *run) sh(cmd string) (string, error) {
	ctx, cancel := context.WithTimeout(x.ctx, 3*time.Minute)
	defer cancel()
	return sshx.Run(ctx, x.t, cmd)
}

func (x *run) shIn(cmd, input string) (string, error) {
	ctx, cancel := context.WithTimeout(x.ctx, 3*time.Minute)
	defer cancel()
	return sshx.RunInput(ctx, x.t, cmd, strings.NewReader(input))
}

// shOK runs a command and ignores failure, returning whatever it printed.
func (x *run) shOK(cmd string) string {
	out, _ := x.sh(cmd)
	return out
}

func (x *run) remoteJSON(path string) map[string]any {
	out := x.shOK("cat " + path + " 2>/dev/null")
	m := map[string]any{}
	_ = json.Unmarshal([]byte(out), &m)
	return m
}

// writeRemote atomically replaces a file under the remote home with private permissions.
func (x *run) writeRemote(path, content string) error {
	if x.opts.DryRun {
		return nil
	}
	dir := filepath.ToSlash(filepath.Dir(path))
	_, err := x.shIn(fmt.Sprintf("umask 077 && mkdir -p %s && cat > %s.sky-tmp && mv %s.sky-tmp %s", sq(dir), sq(path), sq(path), sq(path)), content)
	return err
}

// rewrite points this computer's home paths at the machine's home.
func (x *run) rewrite(s string) string {
	if x.home == x.remoteHome {
		return s
	}
	s = strings.ReplaceAll(s, x.home+"/", x.remoteHome+"/")
	return strings.ReplaceAll(s, filepath.ToSlash(x.home)+"/", x.remoteHome+"/")
}

const shaFn = `h(){ if command -v sha256sum >/dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }; `

// remoteHashes returns sha256 per path (relative to base) for the given files.
func (x *run) remoteHashes(base string, files []string) map[string]string {
	out := map[string]string{}
	if len(files) == 0 {
		return out
	}
	const chunk = 400 // keep command lines a sane length
	for i := 0; i < len(files); i += chunk {
		j := min(i+chunk, len(files))
		var q []string
		for _, f := range files[i:j] {
			q = append(q, sq(f))
		}
		raw := x.shOK(shaFn + "cd " + base + " 2>/dev/null && h -- " + strings.Join(q, " ") + " 2>/dev/null")
		for _, line := range strings.Split(raw, "\n") {
			sum, name, ok := strings.Cut(line, " ")
			if ok {
				out[strings.TrimLeft(name, " *")] = sum
			}
		}
	}
	return out
}

// file is one file staged for upload.
type file struct {
	Rel  string // path relative to the base directory on the machine, forward slashes
	Data []byte
	Mode os.FileMode
}

func hash(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// push uploads files into base on the machine as one tar stream.
func (x *run) push(base string, files []file) error {
	if x.opts.DryRun || len(files) == 0 {
		return nil
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	dirs := map[string]bool{}
	for _, f := range files {
		for d := pathDir(f.Rel); d != "." && d != "" && !dirs[d]; d = pathDir(d) {
			dirs[d] = true
		}
	}
	var dl []string
	for d := range dirs {
		dl = append(dl, d)
	}
	sort.Strings(dl)
	for _, d := range dl {
		if err := tw.WriteHeader(&tar.Header{Name: d + "/", Mode: 0o700, Typeflag: tar.TypeDir, ModTime: time.Now()}); err != nil {
			return err
		}
	}
	for _, f := range files {
		mode := int64(f.Mode.Perm())
		if mode == 0 {
			mode = 0o600
		}
		if err := tw.WriteHeader(&tar.Header{Name: f.Rel, Mode: mode, Size: int64(len(f.Data)), Typeflag: tar.TypeReg, ModTime: time.Now()}); err != nil {
			return err
		}
		if _, err := tw.Write(f.Data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(x.ctx, 5*time.Minute)
	defer cancel()
	_, err := sshx.RunInput(ctx, x.t, "mkdir -p "+base+" && tar -xpf - -C "+base+" 2>&1", &buf)
	return err
}

func pathDir(p string) string {
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return "."
	}
	return p[:i]
}

// isText guesses whether a file is safe to rewrite paths in.
func isText(b []byte) bool {
	return utf8.Valid(b) && !bytes.ContainsRune(b, 0)
}

// sq single-quotes for the remote shell; paths starting with ~/ keep the tilde unquoted.
func sq(s string) string {
	if strings.HasPrefix(s, "~/") {
		return `~/` + sshx.Quote(s[2:])
	}
	if s == "~" {
		return "~"
	}
	return sshx.Quote(s)
}

func digest(v any) string {
	b, _ := json.Marshal(v) // map keys marshal sorted
	return hash(b)[:16]
}

func deepMerge(base, over map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		if ov, ok := v.(map[string]any); ok {
			if bv, ok := out[k].(map[string]any); ok {
				out[k] = deepMerge(bv, ov)
				continue
			}
		}
		out[k] = v
	}
	return out
}

func readJSON(path string) map[string]any {
	m := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func equalJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
