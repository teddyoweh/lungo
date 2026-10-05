// Package claudeacct tracks the Claude accounts machines can sign in with: their tokens,
// how much of each usage window they have used, and when a limited one resets.
//
// Usage comes from Claude Code itself. A tiny `claude -p` call with the account's token
// (no tools, a one-line system prompt, the smallest model) emits a rate_limit_event with
// the 5-hour and weekly utilization and reset times. Long-lived tokens from
// `claude setup-token` can't read Anthropic's usage API, but they can do this.
package claudeacct

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"skybuild/internal/osx"
	"skybuild/internal/paths"
	"skybuild/internal/secret"
)

// States an account can be in.
const (
	StateOK      = "ok"      // can be used
	StateWarning = "warning" // allowed, but close to a limit
	StateLimited = "limited" // a usage window is used up until ResetsAt
	StateInvalid = "invalid" // the token was rejected
	StateError   = "error"   // the check itself failed (network, Claude Code missing)
	StateUnknown = ""        // never checked
)

// Window is one usage window (five_hour, seven_day…).
type Window struct {
	Utilization float64   `json:"utilization"` // 0..1+, fraction used
	ResetsAt    time.Time `json:"resetsAt"`
}

// Status is what the last check found for an account.
type Status struct {
	State     string             `json:"state"`
	LimitType string             `json:"limitType,omitempty"` // which window is used up
	ResetsAt  time.Time          `json:"resetsAt,omitempty"`  // when it can be used again
	Windows   map[string]*Window `json:"windows,omitempty"`
	Message   string             `json:"message,omitempty"`
	CheckedAt time.Time          `json:"checkedAt"`
}

// Usable reports whether machines can sign in with the account now.
func (s Status) Usable() bool {
	switch s.State {
	case StateLimited:
		return !s.ResetsAt.IsZero() && time.Now().After(s.ResetsAt)
	case StateInvalid:
		return false
	}
	return true
}

// Token returns an account's token from the keychain.
func Token(id string) string { return secret.Get(secret.ClaudeTokenPrefix + id) }

// SetToken stores an account's token.
func SetToken(id, token string) error { return secret.Set(secret.ClaudeTokenPrefix+id, token) }

func statePath() string { return filepath.Join(paths.State(), "claude-accounts.json") }

// Load reads every account's last status.
func Load() map[string]Status {
	out := map[string]Status{}
	if b, err := os.ReadFile(statePath()); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

// Save records one account's status (under a lock: the desktop app and the background
// agent both check accounts).
func Save(id string, st Status) error {
	if err := paths.Ensure(); err != nil {
		return err
	}
	l := flock.New(statePath() + ".lock")
	if err := l.Lock(); err != nil {
		return err
	}
	defer l.Unlock()
	all := Load()
	all[id] = st
	b, _ := json.MarshalIndent(all, "", "  ")
	tmp := statePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, statePath())
}

// Forget drops an account's status.
func Forget(id string) {
	l := flock.New(statePath() + ".lock")
	if l.Lock() != nil {
		return
	}
	defer l.Unlock()
	all := Load()
	delete(all, id)
	b, _ := json.MarshalIndent(all, "", "  ")
	_ = os.WriteFile(statePath(), b, 0o600)
}

type rateEvent struct {
	Type string `json:"type"`
	Info *struct {
		Status         string `json:"status"`
		ResetsAt       int64  `json:"resetsAt"`
		RateLimitType  string `json:"rateLimitType"`
		UnifiedWindows map[string]struct {
			Utilization float64 `json:"utilization"`
			ResetsAt    int64   `json:"resetsAt"`
		} `json:"unifiedWindows"`
	} `json:"rate_limit_info"`
	IsError        bool   `json:"is_error"`
	APIErrorStatus int    `json:"api_error_status"`
	Result         string `json:"result"`
}

// Probe checks an account by making the smallest possible Claude Code request with its
// token, in a throwaway config directory so this computer's own Claude setup is untouched.
func Probe(ctx context.Context, token string) Status { return probe(ctx, token) }

// ProbeLocal makes the same request with this computer's own Claude Code login. Its usage
// windows identify which account the login is (see SameAccount).
func ProbeLocal(ctx context.Context) Status { return probe(ctx, "") }

func probe(ctx context.Context, token string) Status {
	st := Status{CheckedAt: time.Now()}
	if !osx.Has("claude") {
		st.State, st.Message = StateError, "Claude Code isn't installed on this computer"
		return st
	}
	dir, err := os.MkdirTemp("", "sky-claude-probe-*")
	if err != nil {
		st.State, st.Message = StateError, err.Error()
		return st
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, osx.Which("claude"), "-p", "Reply OK",
		"--model", "haiku", "--output-format", "stream-json", "--verbose",
		"--system-prompt", "Reply OK.", "--tools", "", "--strict-mcp-config",
		"--setting-sources", "", "--no-session-persistence")
	cmd.Dir = dir
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CLAUDE_CODE_OAUTH_TOKEN=") && !strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") && !strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
			env = append(env, kv)
		}
	}
	env = append(env, "DISABLE_AUTOUPDATER=1")
	if token != "" {
		env = append(env, "CLAUDE_CONFIG_DIR="+dir, "CLAUDE_CODE_OAUTH_TOKEN="+token)
	}
	cmd.Env = env
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.StdoutPipe()
	if err != nil {
		st.State, st.Message = StateError, err.Error()
		return st
	}
	if err := cmd.Start(); err != nil {
		st.State, st.Message = StateError, err.Error()
		return st
	}
	var gotRate bool
	var result *rateEvent
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var ev rateEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "rate_limit_event":
			if ev.Info != nil {
				gotRate = true
				applyRate(&st, ev)
			}
		case "result":
			e := ev
			result = &e
		}
	}
	_ = cmd.Wait()
	if result != nil && result.IsError {
		switch {
		case result.APIErrorStatus == 401 || result.APIErrorStatus == 403:
			st.State, st.Message = StateInvalid, "Token rejected: "+trim(result.Result)
			return st
		case result.APIErrorStatus == 429 || strings.Contains(strings.ToLower(result.Result), "limit"):
			st.State = StateLimited
			st.Message = trim(result.Result)
			return st
		case !gotRate:
			st.State, st.Message = StateError, trim(result.Result)
			return st
		}
	}
	if !gotRate && result == nil {
		st.State, st.Message = StateError, "Claude Code gave no answer"
		if ctx.Err() != nil {
			st.Message = "the check timed out"
		}
	}
	return st
}

func applyRate(st *Status, ev rateEvent) {
	i := ev.Info
	st.Windows = map[string]*Window{}
	for name, w := range i.UnifiedWindows {
		st.Windows[name] = &Window{Utilization: w.Utilization, ResetsAt: unix(w.ResetsAt)}
	}
	switch i.Status {
	case "rejected":
		st.State = StateLimited
		st.LimitType = i.RateLimitType
		st.ResetsAt = unix(i.ResetsAt)
	case "allowed_warning":
		st.State = StateWarning
	default:
		st.State = StateOK
	}
}

func unix(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

func trim(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 240 {
		s = s[:240] + "…"
	}
	return s
}

// SameAccount reports whether two statuses look like the same account: the weekly window
// resets at the same moment and both windows are used about as much. Reset times are set per
// account, so this tells accounts apart without the profile access these tokens lack.
func SameAccount(a, b Status) bool {
	wa, wb := a.Windows["seven_day"], b.Windows["seven_day"]
	if wa == nil || wb == nil || wa.ResetsAt.IsZero() || !wa.ResetsAt.Equal(wb.ResetsAt) {
		return false
	}
	if d := wa.Utilization - wb.Utilization; d > 0.02 || d < -0.02 {
		return false
	}
	fa, fb := a.Windows["five_hour"], b.Windows["five_hour"]
	if fa != nil && fb != nil {
		if d := fa.Utilization - fb.Utilization; d > 0.05 || d < -0.05 {
			return false
		}
	}
	return true
}

// Plan turns Claude Code's account details into a short plan name ("Max 20x").
func Plan(oauth map[string]any) string {
	typ, _ := oauth["organizationType"].(string)
	tier, _ := oauth["organizationRateLimitTier"].(string)
	if t, _ := oauth["userRateLimitTier"].(string); t != "" {
		tier = t
	}
	name := map[string]string{"claude_max": "Max", "claude_pro": "Pro", "claude_team": "Team", "claude_enterprise": "Enterprise", "claude_free": "Free"}[typ]
	for _, x := range []string{"20x", "5x"} {
		if strings.Contains(tier, x) {
			if name == "" {
				name = "Max"
			}
			return name + " " + x
		}
	}
	return name
}

// WindowLabel names a usage window for people.
func WindowLabel(name string) string {
	switch name {
	case "five_hour":
		return "5-hour"
	case "seven_day":
		return "weekly"
	case "seven_day_opus":
		return "weekly Opus"
	case "seven_day_sonnet":
		return "weekly Sonnet"
	}
	return strings.ReplaceAll(name, "_", " ")
}

// Describe is a one-line summary of a status.
func Describe(st Status) string {
	switch st.State {
	case StateLimited:
		if !st.ResetsAt.IsZero() {
			return fmt.Sprintf("%s limit reached, resets %s", WindowLabel(st.LimitType), st.ResetsAt.Local().Format("Mon 3:04 PM"))
		}
		return "limit reached"
	case StateInvalid:
		return "token rejected"
	case StateError:
		return "couldn't check: " + st.Message
	case StateUnknown:
		return "not checked yet"
	}
	var parts []string
	for _, name := range []string{"five_hour", "seven_day"} {
		if w := st.Windows[name]; w != nil {
			parts = append(parts, fmt.Sprintf("%s %.0f%%", WindowLabel(name), w.Utilization*100))
		}
	}
	if len(parts) == 0 {
		return "ready"
	}
	return strings.Join(parts, " · ")
}

// Attempt records that something is being tried now and reports whether it is allowed: false
// when the same key was attempted less than every ago. It survives across processes (the
// desktop app and the background agent share it).
func Attempt(key string, every time.Duration) bool {
	if err := paths.Ensure(); err != nil {
		return true
	}
	path := filepath.Join(paths.State(), "claude-attempts.json")
	l := flock.New(path + ".lock")
	if l.Lock() != nil {
		return true
	}
	defer l.Unlock()
	all := map[string]int64{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &all)
	}
	now := time.Now()
	if last, ok := all[key]; ok && every > 0 && now.Sub(time.Unix(last, 0)) < every {
		return false
	}
	for k, t := range all { // forget old entries
		if now.Sub(time.Unix(t, 0)) > 24*time.Hour {
			delete(all, k)
		}
	}
	all[key] = now.Unix()
	b, _ := json.Marshal(all)
	_ = os.WriteFile(path, b, 0o600)
	return true
}
