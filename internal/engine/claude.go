package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"

	"skybuild/internal/claudeacct"
	"skybuild/internal/config"
	"skybuild/internal/events"
	"skybuild/internal/model"
	"skybuild/internal/paths"
	"skybuild/internal/sshx"
	"skybuild/internal/syncer"
)

// ClaudeAccountView is an account with its last status, for lists.
type ClaudeAccountView struct {
	Account  *model.ClaudeAccount `json:"account"`
	Status   claudeacct.Status    `json:"status"`
	Active   bool                 `json:"active"`
	Next     bool                 `json:"next"` // machines switch to this one when the active one is limited
	HasToken bool                 `json:"hasToken"`
	Summary  string               `json:"summary"`
	Plan     string               `json:"plan,omitempty"`   // "Max 20x"
	Person   string               `json:"person,omitempty"` // display name on the account
	Org      string               `json:"org,omitempty"`
	Local    bool                 `json:"local"` // the account this computer's Claude Code is signed in to
	Rank     int                  `json:"rank"`  // 1 = use first
	Reason   string               `json:"reason"`
	Suggest  bool                 `json:"suggest"` // sky would switch to this one (shown when auto-switch is off)
}

// ClaudeAccounts lists the account pool in priority order. The first time, it adopts the
// token made for this computer's own account so existing setups carry over.
func (e *Engine) ClaudeAccounts() ([]ClaudeAccountView, error) {
	if err := e.adoptLocalAccount(); err != nil {
		return nil, err
	}
	c, err := config.Load()
	if err != nil {
		return nil, err
	}
	statuses := claudeacct.Load()
	active := activeID(c)
	has := func(id string) bool { return claudeacct.Token(id) != "" }
	ranked := rankAccounts(c, statuses, has)
	byID := map[string]claudeacct.Ranked{}
	rankOf := map[string]int{}
	for i, r := range ranked {
		byID[r.ID], rankOf[r.ID] = r, i+1
	}
	nextID, suggestID := "", ""
	if c.Settings.ClaudeSmart() {
		for _, r := range ranked {
			if r.Usable && r.ID != active {
				nextID = r.ID
				break
			}
		}
		if best := smartPick(ranked, active); best != "" && best != active && !c.Settings.ClaudeAutoOn() {
			suggestID = best
		}
	} else if n := pickNext(c.ClaudeAccounts, statuses, active, has); n != nil {
		nextID = n.ID
	}
	localEmail := ""
	if l := syncer.LocalAccount(paths.Home()); l != nil {
		localEmail = l.Email()
	}
	var out []ClaudeAccountView
	for _, a := range c.ClaudeAccounts {
		st := statuses[a.ID]
		v := ClaudeAccountView{Account: a, Status: st, Active: a.ID == active, Next: a.ID == nextID, Suggest: a.ID == suggestID,
			HasToken: has(a.ID), Summary: claudeacct.Describe(st), Local: a.Email != "" && a.Email == localEmail,
			Rank: rankOf[a.ID], Reason: byID[a.ID].Reason}
		if a.OAuthAccount != nil {
			v.Plan = claudeacct.Plan(a.OAuthAccount)
			v.Person, _ = a.OAuthAccount["displayName"].(string)
			v.Org, _ = a.OAuthAccount["organizationName"].(string)
		}
		out = append(out, v)
	}
	if c.Settings.ClaudeSmart() { // show them in the order they'd be used
		sort.SliceStable(out, func(i, j int) bool { return out[i].Rank < out[j].Rank })
	}
	return out, nil
}

// rankAccounts scores every account (see claudeacct.Rank).
func rankAccounts(c *config.Config, statuses map[string]claudeacct.Status, has func(string) bool) []claudeacct.Ranked {
	ids := make([]string, 0, len(c.ClaudeAccounts))
	plans, skip := map[string]string{}, map[string]bool{}
	for _, a := range c.ClaudeAccounts {
		ids = append(ids, a.ID)
		if a.OAuthAccount != nil {
			plans[a.ID] = claudeacct.Plan(a.OAuthAccount)
		}
		if a.Disabled || !has(a.ID) {
			skip[a.ID] = true
		}
	}
	return claudeacct.Rank(ids, statuses, plans, skip, time.Now())
}

// smartPick is the account machines should use in smart mode: the active one unless it
// can't be used, or another has clearly more to lose (15% margin, so two similar accounts
// don't trade places every minute as they are used).
func smartPick(ranked []claudeacct.Ranked, active string) string {
	var best, cur *claudeacct.Ranked
	for i := range ranked {
		r := &ranked[i]
		if r.ID == active {
			cur = r
		}
		if best == nil && r.Usable {
			best = r
		}
	}
	if best == nil {
		return ""
	}
	if cur == nil || !cur.Usable {
		return best.ID
	}
	if best.ID != cur.ID && best.Score > cur.Score*1.15 {
		return best.ID
	}
	return cur.ID
}

// activeID is the account machines should use: the chosen one if it still exists and is
// enabled, else the first enabled account.
func activeID(c *config.Config) string {
	for _, a := range c.ClaudeAccounts {
		if a.ID == c.Settings.ClaudeActive && !a.Disabled {
			return a.ID
		}
	}
	for _, a := range c.ClaudeAccounts {
		if !a.Disabled {
			return a.ID
		}
	}
	return ""
}

func (e *Engine) adoptLocalAccount() error {
	c, err := config.Load()
	if err != nil || len(c.ClaudeAccounts) > 0 {
		return err
	}
	acct := syncer.LocalAccount(paths.Home())
	if acct == nil || claudeacct.Token(acct.UUID()) == "" {
		return nil
	}
	return config.Update(func(c *config.Config) error {
		if len(c.ClaudeAccounts) == 0 {
			c.ClaudeAccounts = append(c.ClaudeAccounts, &model.ClaudeAccount{
				ID: acct.UUID(), Label: acct.Email(), Email: acct.Email(), AddedAt: time.Now(), OAuthAccount: acct,
			})
		}
		return nil
	})
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "acct-" + hex.EncodeToString(b)
}

// AddClaudeAccount adds an account to the pool. With an empty token it runs
// `claude setup-token`, which signs in with whatever claude.ai account the browser is on.
func (e *Engine) AddClaudeAccount(ctx context.Context, label, token string, r events.Reporter) (*model.ClaudeAccount, error) {
	_ = e.adoptLocalAccount()
	token = strings.TrimSpace(token)
	if token == "" {
		events.Stepf(r, "Creating a token (approve it in your browser as the account you're adding)")
		t, err := syncer.MintToken(ctx)
		if err != nil {
			return nil, err
		}
		token = t
	}
	if !strings.HasPrefix(token, "sk-ant-oat01-") || len(token) < 60 {
		return nil, errors.New("that isn't a Claude Code token (they start with sk-ant-oat01-; make one with `claude setup-token`)")
	}
	c, err := config.Load()
	if err != nil {
		return nil, err
	}
	for _, a := range c.ClaudeAccounts {
		if claudeacct.Token(a.ID) == token {
			return nil, fmt.Errorf("that token is already saved as %s", a.Name())
		}
	}
	events.Stepf(r, "Checking the token")
	st := claudeacct.Probe(ctx, token)
	if st.State == claudeacct.StateInvalid {
		return nil, errors.New(st.Message)
	}
	a := &model.ClaudeAccount{ID: newID(), Label: strings.TrimSpace(label), AddedAt: time.Now()}
	if a.Label == "" {
		a.Label = fmt.Sprintf("Account %d", len(c.ClaudeAccounts)+1)
	}
	if err := claudeacct.SetToken(a.ID, token); err != nil {
		return nil, err
	}
	_ = claudeacct.Save(a.ID, st)
	if err := config.Update(func(c *config.Config) error {
		c.ClaudeAccounts = append(c.ClaudeAccounts, a)
		return nil
	}); err != nil {
		return nil, err
	}
	events.Donef(r, "Added %s (%s)", a.Label, claudeacct.Describe(st))
	_ = e.IdentifyClaudeAccounts(ctx, r)
	if c, err := config.Load(); err == nil {
		for _, x := range c.ClaudeAccounts {
			if x.ID == a.ID {
				a = x
			}
		}
	}
	return a, nil
}

var defaultLabel = regexp.MustCompile(`^(Account \d+)?$`)

// IdentifyClaudeAccounts gives nameless accounts their real identity. A token can't read
// its own profile, but this computer's Claude Code login can: when the login's usage windows
// match an account's, they are the same account, and it takes the login's email, name and plan.
func (e *Engine) IdentifyClaudeAccounts(ctx context.Context, r events.Reporter) error {
	local := syncer.LocalAccount(paths.Home())
	if local == nil {
		return nil
	}
	c, err := config.Load()
	if err != nil {
		return err
	}
	var unnamed []*model.ClaudeAccount
	for _, a := range c.ClaudeAccounts {
		if a.Email == local.Email() || a.ID == local.UUID() {
			return nil // the signed-in account is already known
		}
		if a.Email == "" {
			unnamed = append(unnamed, a)
		}
	}
	if len(unnamed) == 0 {
		return nil
	}
	mine := claudeacct.ProbeLocal(ctx)
	if mine.Windows == nil {
		return nil
	}
	for _, a := range unnamed {
		st := claudeacct.Probe(ctx, claudeacct.Token(a.ID)) // fresh, so both sides are from the same minute
		_ = claudeacct.Save(a.ID, st)
		if !claudeacct.SameAccount(mine, st) {
			continue
		}
		id := a.ID
		err := config.Update(func(c *config.Config) error {
			for _, x := range c.ClaudeAccounts {
				if x.ID == id {
					x.Email, x.OAuthAccount = local.Email(), local
					if defaultLabel.MatchString(x.Label) {
						x.Label = local.Email()
					}
				}
			}
			return nil
		})
		if err == nil {
			events.Infof(r, "Recognised %s as %s", a.Name(), local.Email())
		}
		return err
	}
	return nil
}

func (e *Engine) findAccount(c *config.Config, key string) (*model.ClaudeAccount, int, error) {
	k := strings.ToLower(strings.TrimSpace(key))
	for i, a := range c.ClaudeAccounts {
		if a.ID == key || strings.ToLower(a.Label) == k || strings.ToLower(a.Email) == k {
			return a, i, nil
		}
	}
	var names []string
	for _, a := range c.ClaudeAccounts {
		names = append(names, a.Name())
	}
	return nil, -1, fmt.Errorf("no Claude account %q (have: %s)", key, strings.Join(names, ", "))
}

// RemoveClaudeAccount forgets an account and its token.
func (e *Engine) RemoveClaudeAccount(key string) error {
	var id string
	err := config.Update(func(c *config.Config) error {
		a, i, err := e.findAccount(c, key)
		if err != nil {
			return err
		}
		id = a.ID
		c.ClaudeAccounts = append(c.ClaudeAccounts[:i], c.ClaudeAccounts[i+1:]...)
		if c.Settings.ClaudeActive == id {
			c.Settings.ClaudeActive = ""
		}
		return nil
	})
	if err != nil {
		return err
	}
	_ = claudeacct.SetToken(id, "")
	claudeacct.Forget(id)
	return nil
}

// RenameClaudeAccount changes an account's label.
func (e *Engine) RenameClaudeAccount(key, label string) error {
	return config.Update(func(c *config.Config) error {
		a, _, err := e.findAccount(c, key)
		if err != nil {
			return err
		}
		a.Label = strings.TrimSpace(label)
		return nil
	})
}

// MoveClaudeAccount shifts an account up (negative) or down in the switching order.
func (e *Engine) MoveClaudeAccount(key string, delta int) error {
	return config.Update(func(c *config.Config) error {
		_, i, err := e.findAccount(c, key)
		if err != nil {
			return err
		}
		j := i + delta
		if j < 0 || j >= len(c.ClaudeAccounts) {
			return nil
		}
		c.ClaudeAccounts[i], c.ClaudeAccounts[j] = c.ClaudeAccounts[j], c.ClaudeAccounts[i]
		return nil
	})
}

// SetClaudeAccountEnabled takes an account out of (or back into) rotation.
func (e *Engine) SetClaudeAccountEnabled(key string, on bool) error {
	return config.Update(func(c *config.Config) error {
		a, _, err := e.findAccount(c, key)
		if err != nil {
			return err
		}
		a.Disabled = !on
		return nil
	})
}

// SetClaudeStrategy picks how accounts are ordered: "smart" or "order".
func (e *Engine) SetClaudeStrategy(strategy string) error {
	if strategy != "smart" && strategy != "order" {
		return fmt.Errorf("strategy is smart or order")
	}
	return e.UpdateSettings(func(s *config.Settings) { s.ClaudeStrategy = strategy })
}

// SetClaudeAutoSwitch turns automatic switching on or off.
func (e *Engine) SetClaudeAutoSwitch(on bool) error {
	return e.UpdateSettings(func(s *config.Settings) { s.ClaudeAuto = &on })
}

// UseClaudeAccount makes an account the active one and signs every machine in with it.
func (e *Engine) UseClaudeAccount(ctx context.Context, key string, r events.Reporter) error {
	var name string
	if err := config.Update(func(c *config.Config) error {
		a, _, err := e.findAccount(c, key)
		if err != nil {
			return err
		}
		if a.Disabled {
			return fmt.Errorf("%s is turned off; turn it on first", a.Name())
		}
		c.Settings.ClaudeActive, name = a.ID, a.Name()
		return nil
	}); err != nil {
		return err
	}
	events.Stepf(r, "Signing machines in as %s", name)
	res := e.Sync(ctx, nil, syncer.Options{Only: []string{syncer.ItemClaudeLogin}}, r)
	n := 0
	for _, x := range res {
		if x.Skipped == "" && len(x.Errors) == 0 {
			n++
		}
	}
	events.Donef(r, "%d machine(s) now use %s", n, name)
	return nil
}

// CheckClaudeAccounts checks every account's usage now.
func (e *Engine) CheckClaudeAccounts(ctx context.Context, r events.Reporter) ([]ClaudeAccountView, error) {
	_ = e.IdentifyClaudeAccounts(ctx, r)
	views, err := e.ClaudeAccounts()
	if err != nil {
		return nil, err
	}
	var wg sync.WaitGroup
	for _, v := range views {
		if !v.HasToken {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			st := claudeacct.Probe(ctx, claudeacct.Token(v.Account.ID))
			_ = claudeacct.Save(v.Account.ID, st)
			events.Infof(r, "%s: %s", v.Account.Name(), claudeacct.Describe(st))
		}()
	}
	wg.Wait()
	return e.ClaudeAccounts()
}

// activeLogin is the account machines should sign in with, for sync.
func (e *Engine) activeLogin() *syncer.ClaudeLogin {
	_ = e.adoptLocalAccount()
	c, err := config.Load()
	if err != nil {
		return nil
	}
	id := activeID(c)
	for _, a := range c.ClaudeAccounts {
		if a.ID == id {
			if t := claudeacct.Token(a.ID); t != "" {
				return &syncer.ClaudeLogin{ID: a.ID, Name: a.Name(), Token: t, OAuthAccount: a.OAuthAccount}
			}
		}
	}
	return nil
}

// TickResult says what a rotation pass did.
type TickResult struct {
	Switched   bool      `json:"switched"`
	From       string    `json:"from,omitempty"`
	To         string    `json:"to,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	Restarted  int       `json:"restarted"`      // sessions resumed on the new account
	AllLimited bool      `json:"allLimited"`     // nothing to switch to
	Next       time.Time `json:"next,omitempty"` // earliest reset when everything is limited
}

var limitScreen = regexp.MustCompile(`(?i)(hit your|reached).{0,40}limit|limit (reached|hit)|usage limit`)

// IsLimitMessage reports whether a session message means Claude stopped at a usage limit.
func IsLimitMessage(s string) bool { return limitScreen.MatchString(s) }

// ClaudeTick is one rotation pass, run every minute by the desktop app and the background
// agent:
//   - a Claude session showing a limit message triggers a check of the active account;
//   - accounts are re-checked on a schedule (the active one every 15 minutes, limited ones
//     right after they reset, the rest hourly);
//   - when the active account is limited or rejected and auto-switch is on, machines move to
//     the next usable account and sessions stuck on the limit resume on it.
//
// sessions may be nil, in which case they are listed here.
func (e *Engine) ClaudeTick(ctx context.Context, sessions []Session, r events.Reporter) (TickResult, error) {
	var res TickResult
	l := flock.New(filepath.Join(paths.State(), "claude-tick.lock"))
	if ok, _ := l.TryLock(); !ok {
		return res, nil
	}
	defer l.Unlock()

	views, err := e.ClaudeAccounts()
	if err != nil || len(views) == 0 {
		return res, err
	}
	c, err := config.Load()
	if err != nil {
		return res, err
	}
	active := activeID(c)
	statuses := claudeacct.Load()

	if sessions == nil {
		sessions, _ = e.AllSessions(ctx)
	}
	var blocked []Session
	for _, s := range sessions {
		if s.Claude && s.State == "waiting" && IsLimitMessage(s.Message) {
			blocked = append(blocked, s)
		}
	}

	// Which accounts are due for a check.
	now := time.Now()
	due := map[string]bool{}
	for _, v := range views {
		if !v.HasToken || v.Account.Disabled {
			continue
		}
		st := statuses[v.Account.ID]
		age := now.Sub(st.CheckedAt)
		switch {
		case st.CheckedAt.IsZero():
			due[v.Account.ID] = true
		case st.State == claudeacct.StateLimited && !st.ResetsAt.IsZero() && now.After(st.ResetsAt.Add(30*time.Second)) && age > time.Minute:
			due[v.Account.ID] = true
		case v.Account.ID == active && (age > 15*time.Minute || len(blocked) > 0 && age > 2*time.Minute):
			due[v.Account.ID] = true
		case age > time.Hour:
			due[v.Account.ID] = true
		}
	}
	for id := range due {
		st := claudeacct.Probe(ctx, claudeacct.Token(id))
		if st.State == claudeacct.StateError {
			prev := statuses[id]
			prev.Message, prev.CheckedAt = st.Message, st.CheckedAt
			st = prev // keep what we knew; a failed check says nothing about the account
		}
		statuses[id] = st
		_ = claudeacct.Save(id, st)
	}

	if !c.Settings.ClaudeAutoOn() || active == "" {
		return res, nil
	}
	has := func(id string) bool { return claudeacct.Token(id) != "" }
	cur := statuses[active]
	name := func(id string) string {
		for _, a := range c.ClaudeAccounts {
			if a.ID == id {
				return a.Name()
			}
		}
		return id
	}

	// Decide who machines should be on.
	target, voluntary := active, false
	if c.Settings.ClaudeSmart() {
		ranked := rankAccounts(c, statuses, has)
		if best := smartPick(ranked, active); best != "" && best != active {
			target = best
			voluntary = cur.Usable() // the active one still works; this is a better use of allowances
		} else if best == "" && !cur.Usable() {
			target = ""
		}
	} else if !cur.Usable() {
		if next := pickNext(c.ClaudeAccounts, statuses, active, has); next != nil {
			target = next.ID
		} else {
			target = ""
		}
	}

	if target == active {
		// Staying put. A session still showing a limit is running on an account we already
		// moved away from: resume it (once per half hour, in case it can't help).
		if cur.State == claudeacct.StateOK || cur.State == claudeacct.StateWarning {
			for _, s := range blocked {
				if !claudeacct.Attempt("resume:"+s.Key(), 30*time.Minute) {
					continue
				}
				if err := e.resumeSession(ctx, s); err == nil {
					res.Restarted++
					events.Infof(r, "Resumed %s on %s", s.Name, s.Machine)
				}
			}
		}
		return res, nil
	}
	if target == "" {
		res.AllLimited = true
		for _, st := range statuses {
			if st.State == claudeacct.StateLimited && !st.ResetsAt.IsZero() && (res.Next.IsZero() || st.ResetsAt.Before(res.Next)) {
				res.Next = st.ResetsAt
			}
		}
		return res, nil
	}
	// A voluntary move (better use of allowances, nothing is broken) happens at most every
	// 20 minutes; running sessions keep their account until they hit a limit.
	if voluntary && !claudeacct.Attempt("claude-switch", 20*time.Minute) {
		return res, nil
	}
	res.Switched, res.From, res.To = true, name(active), name(target)
	if voluntary {
		res.Reason = "more of " + res.To + "'s allowance expires sooner"
		events.Stepf(r, "Moving machines to %s: its allowance expires sooner", res.To)
	} else {
		claudeacct.Attempt("claude-switch", 0)
		res.Reason = claudeacct.Describe(cur)
		events.Stepf(r, "%s: %s. Switching machines to %s", res.From, res.Reason, res.To)
	}
	if err := e.UseClaudeAccount(ctx, target, r); err != nil {
		return res, err
	}
	for _, s := range blocked {
		claudeacct.Attempt("resume:"+s.Key(), 0)
		if err := e.resumeSession(ctx, s); err == nil {
			res.Restarted++
			events.Infof(r, "Resumed %s on %s with %s", s.Name, s.Machine, res.To)
		}
	}
	return res, nil
}

// pickNext is the first usable account after active in priority order, wrapping around:
// enabled, with a token, not rejected, and not limited (or past its reset).
func pickNext(accts []*model.ClaudeAccount, statuses map[string]claudeacct.Status, active string, hasToken func(string) bool) *model.ClaudeAccount {
	idx := -1
	for i, a := range accts {
		if a.ID == active {
			idx = i
		}
	}
	for k := 1; k <= len(accts); k++ {
		a := accts[(idx+k+len(accts))%len(accts)]
		if a.ID == active {
			continue
		}
		st := statuses[a.ID]
		if a.Disabled || !hasToken(a.ID) || !st.Usable() || st.State == claudeacct.StateError {
			continue
		}
		return a
	}
	return nil
}

// resumeSession restarts a Claude session stuck on a limit prompt so it picks up the new
// token, resuming the same conversation (by its session ID when the hook recorded one).
func (e *Engine) resumeSession(ctx context.Context, s Session) error {
	m, err := e.Machine(s.Machine)
	if err != nil {
		return err
	}
	q := sshx.Quote(s.Name)
	script := `s=` + q + `
pane=$(tmux display -p -t "$s" '#{pane_pid}') || exit 1
args=$(ps -A -o ppid=,args= | awk -v p="$pane" '$1==p {$1=""; print}' | grep -m1 claude)
sid=$(sed -n 's/.*"sid":"\([^"]*\)".*/\1/p' "$HOME/.skybuild/status/$s.json" 2>/dev/null)
tmux send-keys -t "$s" Escape; sleep 0.6
tmux send-keys -t "$s" C-c; sleep 0.4; tmux send-keys -t "$s" C-c; sleep 2
case "$(tmux display -p -t "$s" '#{pane_current_command}')" in *claude*|node|[0-9]*.[0-9]*.[0-9]*) echo still-running; exit 1;; esac
flags=""
case "$args" in *--dangerously-skip-permissions*) flags=" --dangerously-skip-permissions";; esac
if [ -n "$sid" ]; then tmux send-keys -t "$s" "claude --resume $sid$flags" Enter; else tmux send-keys -t "$s" "claude --continue$flags" Enter; fi
# The session was already running in this folder, so answer Claude's folder-trust prompt
# (it asks again every time in a home directory).
for i in 1 2 3 4 5 6 7 8; do
  sleep 1
  if tmux capture-pane -p -t "$s" | grep -q "trust this folder"; then tmux send-keys -t "$s" Down Enter; break; fi
  tmux capture-pane -p -t "$s" | grep -q "esc to interrupt\|? for shortcuts" && break
done`
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	_, err = sshx.RunInput(ctx, e.Target(m), "bash -s", strings.NewReader(script))
	return err
}
