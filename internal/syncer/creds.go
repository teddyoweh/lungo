package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"skybuild/internal/osx"
	"skybuild/internal/paths"
)

// ---------- git config ----------

var (
	osxKeychainHelper = regexp.MustCompile(`(?m)^\s*helper\s*=\s*(osxkeychain|manager|manager-core|wincred)\s*\n`)
	ghHelperPath      = regexp.MustCompile(`![^\s"]*[/\\]gh(\.exe)?\s+auth\s+git-credential`)
	excludesFile      = regexp.MustCompile(`(?m)^\s*excludesfile\s*=\s*(.+)$`)
)

const ghHelperBlock = "\n[credential \"https://github.com\"]\n\thelper =\n\thelper = !gh auth git-credential\n[credential \"https://gist.github.com\"]\n\thelper =\n\thelper = !gh auth git-credential\n"

// gitConfigFor turns this computer's ~/.gitconfig into one that works on the machine:
// home paths rewritten, Mac/Windows credential helpers dropped and gh referenced by name.
func (x *run) gitConfigFor(local string) string {
	out := x.rewrite(local)
	if x.remoteOS != "darwin" {
		out = osxKeychainHelper.ReplaceAllString(out, "")
	}
	if x.remoteOS != "darwin" { // a Mac has gh at the same Homebrew path; elsewhere find it on PATH
		out = ghHelperPath.ReplaceAllString(out, "!gh auth git-credential")
	}
	if !strings.Contains(out, "gh auth git-credential") && contains(x.m.Sync, ItemGitHub) {
		out = strings.TrimRight(out, "\n") + "\n" + ghHelperBlock
	}
	return out
}

func (x *run) syncGitConfig() error {
	b, err := os.ReadFile(filepath.Join(x.home, ".gitconfig"))
	if err != nil {
		return nil
	}
	files := map[string]string{".gitconfig": x.gitConfigFor(string(b))}
	if m := excludesFile.FindStringSubmatch(string(b)); m != nil {
		p := strings.Trim(strings.TrimSpace(m[1]), `"`)
		p = strings.Replace(p, "~", x.home, 1)
		if rel, err := filepath.Rel(x.home, p); err == nil && !strings.HasPrefix(rel, "..") {
			if eb, err := os.ReadFile(p); err == nil {
				files[filepath.ToSlash(rel)] = string(eb)
			}
		}
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		want := files[name]
		if strings.TrimSpace(x.shOK("cat "+sq(name)+" 2>/dev/null")) == strings.TrimSpace(want) {
			continue
		}
		if name == ".gitconfig" && !x.state.GitBackup && !x.opts.DryRun {
			x.shOK("[ -f .gitconfig ] && [ ! -f .gitconfig.before-sky ] && cp .gitconfig .gitconfig.before-sky")
			x.state.GitBackup = true
		}
		x.change("~/%s updated", name)
		if err := x.writeRemote(name, want); err != nil {
			return err
		}
	}
	return nil
}

// ---------- GitHub ----------

type ghAccount struct {
	Login       string `json:"login"`
	State       string `json:"state"`
	Active      bool   `json:"active"`
	GitProtocol string `json:"gitProtocol"`
}

func parseGH(raw string) map[string][]ghAccount {
	var v struct {
		Hosts map[string][]ghAccount `json:"hosts"`
	}
	_ = json.Unmarshal([]byte(raw), &v)
	return v.Hosts
}

// syncGitHub copies each working gh account's token and matches the active account. The
// machine stores them in hosts.yml (--insecure-storage) because a Linux server or a locked
// Mac keychain has no keyring reachable over SSH.
func (x *run) syncGitHub() error {
	if !osx.Has("gh") {
		return nil
	}
	ctx, cancel := context.WithTimeout(x.ctx, 20*time.Second)
	defer cancel()
	raw, _ := osx.Run(ctx, "gh", "auth", "status", "--json", "hosts")
	local := parseGH(raw)
	if len(local) == 0 {
		return nil
	}
	if strings.TrimSpace(x.shOK("command -v gh")) == "" {
		return fmt.Errorf("gh isn't installed on %s", x.m.Name)
	}
	for host, accts := range local {
		var ok []ghAccount
		for _, a := range accts {
			if a.State == "success" {
				ok = append(ok, a)
			}
		}
		sort.SliceStable(ok, func(i, j int) bool { return !ok[i].Active && ok[j].Active }) // active last
		for _, a := range ok {
			tok, err := osx.Run(ctx, "gh", "auth", "token", "-h", host, "-u", a.Login)
			tok = strings.TrimSpace(tok)
			if err != nil || tok == "" {
				continue
			}
			if strings.TrimSpace(x.shOK("gh auth token -h "+sq(host)+" -u "+sq(a.Login)+" 2>/dev/null")) == tok {
				continue
			}
			x.change("gh %s@%s: token updated", a.Login, host)
			if x.opts.DryRun {
				continue
			}
			proto := a.GitProtocol
			if proto == "" {
				proto = "https"
			}
			if _, err := x.shIn("gh auth login -h "+sq(host)+" -p "+sq(proto)+" --insecure-storage --with-token", tok+"\n"); err != nil {
				return err
			}
		}
		var active string
		for _, a := range ok {
			if a.Active {
				active = a.Login
			}
		}
		remoteActive := ""
		for _, a := range parseGH(x.shOK("gh auth status --json hosts 2>/dev/null"))[host] {
			if a.Active {
				remoteActive = a.Login
			}
		}
		if active != "" && active != remoteActive {
			x.change("gh %s: active account → %s", host, active)
			if !x.opts.DryRun {
				if _, err := x.sh("gh auth switch -h " + sq(host) + " -u " + sq(active)); err != nil {
					return err
				}
			}
		}
	}
	// Without the git config item, wire git to gh here (with it, gitConfigFor already did).
	if !contains(x.m.Sync, ItemGit) && !strings.Contains(x.shOK("cat .gitconfig 2>/dev/null"), "gh auth git-credential") {
		x.change("git: uses gh for GitHub credentials")
		if !x.opts.DryRun {
			x.shOK("gh auth setup-git")
		}
	}
	return nil
}

// ---------- API keys from shell startup files ----------

var (
	secretExport = regexp.MustCompile(`^\s*export\s+([A-Z][A-Z0-9_]*(KEY|TOKEN|SECRET|PASSWORD|AUTH|CREDENTIALS?)[A-Z0-9_]*)=`)
	shellFiles   = []string{".zshrc", ".zprofile", ".zshenv", ".bashrc", ".bash_profile", ".profile"}
	skipEnv      = map[string]bool{"CLAUDE_CODE_OAUTH_TOKEN": true}
)

const secretsLine = `[ -r "$HOME/.secrets.sh" ] && . "$HOME/.secrets.sh"  # skybuild secrets`

// secretNames finds the names of secret-looking exports in the shell startup files.
func (x *run) secretNames() []string { return secretNamesIn(x.home) }

// ShellSecrets returns the API keys exported from this computer's shell startup files, with
// their values, and which file each name came from.
func ShellSecrets(ctx context.Context) (map[string]string, map[string]string) {
	home := paths.Home()
	names := secretNamesIn(home)
	from := map[string]string{}
	for _, f := range shellFiles {
		b, err := os.ReadFile(filepath.Join(home, f))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if m := secretExport.FindStringSubmatch(line); m != nil && from[m[1]] == "" {
				from[m[1]] = "~/" + f
			}
		}
	}
	return secretValues(ctx, names), from
}

func secretNamesIn(home string) []string {
	seen := map[string]bool{}
	var names []string
	for _, f := range shellFiles {
		b, err := os.ReadFile(filepath.Join(home, f))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if m := secretExport.FindStringSubmatch(line); m != nil && !seen[m[1]] && !skipEnv[m[1]] {
				seen[m[1]] = true
				names = append(names, m[1])
			}
		}
	}
	sort.Strings(names)
	return names
}

// secretValues asks the user's login shell for the values, so exports built from command
// substitutions (Keychain lookups, 1Password) arrive as plain values on the machine.
func secretValues(ctx context.Context, names []string) map[string]string {
	out := map[string]string{}
	if len(names) == 0 {
		return out
	}
	if runtime.GOOS == "windows" {
		for _, n := range names {
			if v := os.Getenv(n); v != "" {
				out[n] = v
			}
		}
		return out
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	script := `printf '__SKY_ENV__'; for n in ` + strings.Join(names, " ") + `; do printf '%s=%s\0' "$n" "$(printenv "$n")"; done`
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	r, _ := osx.Exec(ctx, osx.Cmd{Name: shell, Args: []string{"-ilc", script}, Stdin: strings.NewReader("")})
	raw := r.Stdout
	if i := strings.LastIndex(raw, "__SKY_ENV__"); i >= 0 {
		raw = raw[i+len("__SKY_ENV__"):]
	}
	for _, kv := range strings.Split(raw, "\x00") {
		if k, v, ok := strings.Cut(kv, "="); ok && len(v) >= 8 { // shorter is a flag, not a secret
			out[k] = v
		}
	}
	return out
}

func (x *run) syncEnv() error {
	names := x.secretNames()
	vals := secretValues(x.ctx, names)
	for _, k := range x.opts.APIKeys { // keys stored in sky win over shell exports
		if k.Value == "" {
			continue
		}
		on := len(k.Machines) == 0
		for _, m := range k.Machines {
			on = on || m == x.m.Name
		}
		if on {
			vals[k.Name] = k.Value
		} else {
			delete(vals, k.Name)
		}
	}
	for _, n := range x.settings.SkipEnv {
		delete(vals, n)
	}
	if len(vals) == 0 && strings.TrimSpace(x.shOK("cat .secrets.sh 2>/dev/null")) == "" {
		return nil
	}
	var b strings.Builder
	b.WriteString("# Synced by skybuild from your computer; edits here get overwritten.\n")
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "export %s=%s\n", k, sq(x.rewrite(vals[k])))
	}
	content := b.String()
	have := strings.Fields(x.shOK(shaFn + "h .secrets.sh 2>/dev/null"))
	if len(have) == 0 || have[0] != hash([]byte(content)) {
		x.change("~/.secrets.sh: %d key(s)", len(keys))
		if err := x.writeRemote(".secrets.sh", content); err != nil {
			return err
		}
	}
	if err := x.ensureLine(".zshenv", secretsLine, ".secrets.sh", false); err != nil {
		return err
	}
	return x.ensureLine(".bashrc", secretsLine, ".secrets.sh", true)
}

// ---------- credential files ----------

// credSpec is a credential path under ~ and which children to copy (nil = everything).
// Paths can differ per OS (Vercel keeps its login under Application Support on a Mac).
// Label and Icon name the tool for display; Icon matches a brand in the desktop app.
type credSpec struct {
	Path     map[string]string // GOOS → path; "" key = all
	Children []string
	Label    string
	Icon     string
}

func cs(label, icon, path string, children ...string) credSpec {
	return credSpec{Path: map[string]string{"": path}, Children: children, Label: label, Icon: icon}
}

// Left out on purpose: tools whose OAuth refresh tokens rotate (Codex, Factory, Grok,
// Wrangler: sharing them signs one machine out), wallets and SSH keys.
var credentials = []credSpec{
	cs("gcloud", "gcp", ".config/gcloud", "access_tokens.db", "active_config", "application_default_credentials.json",
		"configurations", "credentials.db", "default_configs.db", "legacy_credentials"),
	cs("gsutil", "gcp", ".gsutil", "credstore2"),
	cs("boto", "gcp", ".boto"),
	cs("AWS", "aws", ".aws", "config", "credentials"),
	cs("Docker", "docker", ".docker", "config.json"),
	cs("Kubernetes", "kubernetes", ".kube", "config"),
	cs("npm", "npm", ".npmrc"),
	cs("Yarn", "yarn", ".yarnrc.yml"),
	cs("Git credentials", "git", ".git-credentials"),
	cs("netrc", "", ".netrc"),
	cs("Modal", "modal", ".modal.toml"),
	cs("Fly.io", "fly", ".fly", "config.yml"),
	cs("Railway", "railway", ".railway", "config.json"),
	cs("Cloudflare Tunnel", "cloudflare", ".cloudflared"),
	cs("Stripe", "stripe", ".config/stripe", "config.toml"),
	{Path: map[string]string{"darwin": "Library/Application Support/com.vercel.cli", "": ".local/share/com.vercel.cli"},
		Children: []string{"auth.json", "config.json"}, Label: "Vercel", Icon: "vercel"},
	cs("Expo", "expo", ".expo", "state.json"),
	cs("Blaxel", "", ".blaxel", "config.yaml"),
	cs("Kaggle", "kaggle", ".kaggle", "kaggle.json"),
	cs("App Store Connect", "appstore", ".appstoreconnect"),
	cs("Hugging Face", "huggingface", ".config/hf", "token"),
	cs("Hugging Face", "huggingface", ".cache/huggingface", "token"),
	cs("Supabase", "supabase", ".supabase", "access-token"),
	cs("Firebase", "firebase", ".config/configstore", "firebase-tools.json"),
	cs("PyPI", "pypi", ".pypirc"),
	cs("Cargo", "rust", ".cargo", "credentials.toml"),
}

// Credential is one CLI login sync knows about, as found on this computer.
type Credential struct {
	Path    string `json:"path"`  // under ~, as shown in settings
	Label   string `json:"label"` // tool name
	Icon    string `json:"icon"`  // brand icon name ("" = generic)
	Present bool   `json:"present"`
	Skipped bool   `json:"skipped"` // turned off in settings
}

// LocalCredentials lists every CLI login sync can copy and whether this computer has it.
func LocalCredentials(skip []string) []Credential {
	home := paths.Home()
	var out []Credential
	for _, c := range credentials {
		p := c.pathFor(runtime.GOOS)
		present := false
		if c.Children == nil {
			_, err := os.Stat(filepath.Join(home, filepath.FromSlash(p)))
			present = err == nil
		} else {
			for _, ch := range c.Children {
				if _, err := os.Stat(filepath.Join(home, filepath.FromSlash(p), ch)); err == nil {
					present = true
					break
				}
			}
		}
		out = append(out, Credential{Path: c.Path[""], Label: c.Label, Icon: c.Icon, Present: present, Skipped: contains(skip, c.Path[""])})
	}
	return out
}

// CredentialNames lists the credential paths for display.
func CredentialNames() []string {
	var out []string
	for _, c := range credentials {
		out = append(out, c.Path[""])
	}
	return out
}

func (c credSpec) pathFor(goos string) string {
	if p, ok := c.Path[goos]; ok {
		return p
	}
	return c.Path[""]
}

// homePair is one path to copy: where it is here, where it goes there (both relative to ~).
type homePair struct{ Local, Remote string }

func credentialPaths(x *run) []homePair {
	var out []homePair
	for _, c := range credentials {
		lp, rp := c.pathFor(runtime.GOOS), c.pathFor(x.remoteOS)
		if contains(x.settings.SkipCredentials, c.Path[""]) {
			continue
		}
		if _, err := os.Stat(filepath.Join(x.home, filepath.FromSlash(lp))); err != nil {
			continue
		}
		if c.Children == nil {
			out = append(out, homePair{lp, rp})
			continue
		}
		for _, ch := range c.Children {
			if _, err := os.Stat(filepath.Join(x.home, filepath.FromSlash(lp), ch)); err == nil {
				out = append(out, homePair{lp + "/" + ch, rp + "/" + ch})
			}
		}
	}
	return out
}

func extraPaths(x *run) []homePair {
	var out []homePair
	for _, p := range x.settings.SyncPaths {
		p = strings.TrimPrefix(strings.TrimPrefix(filepath.ToSlash(p), "~/"), "/")
		if p == "" || strings.Contains(p, "..") {
			continue
		}
		if _, err := os.Stat(filepath.Join(x.home, filepath.FromSlash(p))); err == nil {
			out = append(out, homePair{p, p})
		}
	}
	return out
}

// rootOf names the tool a file belongs to: its credential path's top folder (".config/gcloud").
func rootOf(rel string, pairs []homePair) string {
	best := rel
	for _, p := range pairs {
		if rel == p.Remote || strings.HasPrefix(rel, p.Remote+"/") {
			root := p.Remote
			if i := strings.LastIndex(root, "/"); i > 0 && rel != root {
				root = root[:i]
			}
			if len(root) < len(best) {
				best = root
			}
		}
	}
	if i := strings.Index(best, "/"); i > 0 && strings.HasPrefix(best, ".config/") {
		parts := strings.SplitN(best, "/", 3)
		return strings.Join(parts[:min(2, len(parts))], "/")
	}
	return best
}

// syncHomeFiles copies files under ~ with one checksum round trip and one tar stream, so a
// slow link costs seconds. Files on the machine that would be overwritten are backed up once.
func (x *run) syncHomeFiles(pairs []homePair) error {
	var files []file
	for _, p := range pairs {
		got, err := collect(filepath.Join(x.home, filepath.FromSlash(p.Local)), p.Remote)
		if err != nil {
			return err
		}
		for _, f := range got {
			base := filepath.Base(f.Rel)
			if strings.HasSuffix(base, ".bak") || strings.Contains(f.Rel, "/logs/") || strings.Contains(f.Rel, "/cache/") {
				continue
			}
			if isText(f.Data) {
				f.Data = []byte(x.rewrite(string(f.Data)))
			}
			f.Mode = 0o600
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		return nil
	}
	rels := make([]string, len(files))
	for i, f := range files {
		rels[i] = f.Rel
	}
	have := x.remoteHashes("~", rels)
	var changed []file
	var existing []string
	perRoot := map[string]int{}
	var roots []string
	for _, f := range files {
		if have[f.Rel] != hash(f.Data) {
			changed = append(changed, f)
			root := rootOf(f.Rel, pairs)
			if perRoot[root] == 0 {
				roots = append(roots, root)
			}
			perRoot[root]++
			if have[f.Rel] != "" {
				existing = append(existing, f.Rel)
			}
		}
	}
	for _, r := range roots {
		if perRoot[r] == 1 {
			x.change("~/%s updated", r)
		} else {
			x.change("~/%s: %d files updated", r, perRoot[r])
		}
	}
	if len(changed) == 0 || x.opts.DryRun {
		return nil
	}
	if x.state.TokensBackup == "" && len(existing) > 0 {
		backup := ".skybuild-backups/before-sync-" + time.Now().Format("20060102") + ".tgz"
		var q []string
		for _, f := range existing {
			q = append(q, sq(f))
		}
		x.shOK("umask 077 && mkdir -p .skybuild-backups && tar czf " + backup + " -- " + strings.Join(q, " "))
		x.state.TokensBackup = backup
	}
	return x.push("~", changed)
}
