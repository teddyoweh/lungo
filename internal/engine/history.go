package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"skybuild/internal/model"
	"skybuild/internal/paths"
	"skybuild/internal/sshx"
)

// Claude Code's own history.
//
// Claude Code keeps every conversation as a file of JSON lines, one file per conversation,
// in ~/.claude/projects/<folder>/<conversation id>.jsonl on the machine it ran on (<folder>
// is the folder Claude was started in, with everything but letters and digits turned into
// dashes). A conversation can be picked up again with `claude --resume <id>` run in that
// folder. This lists them, newest first, without reading any file whole: a power user has
// thousands of them and some are hundreds of megabytes. One perl script per machine looks at
// the start and the end of each file; what those lines mean is worked out here.

// Conversation is one past Claude Code conversation on a machine.
type Conversation struct {
	Machine  string    `json:"machine"`
	ID       string    `json:"id"`
	Dir      string    `json:"dir"`                // the folder it ran in, where it can be resumed
	Title    string    `json:"title"`              // the name Claude or the user gave it, else what the user first said
	Updated  time.Time `json:"updated"`            // the last thing said in it
	Messages int       `json:"messages,omitempty"` // what the user and Claude said (0: not counted, the file is big)
	Branch   string    `json:"branch,omitempty"`   // git branch it was last on
	Size     int64     `json:"size,omitempty"`     // of its file, in bytes
	Auto     bool      `json:"auto,omitempty"`     // started by a script (the SDK, `claude -p`), not typed by hand
	Live     string    `json:"live,omitempty"`     // the tmux session it is open in right now
	Running  bool      `json:"running,omitempty"`  // a claude process on the machine has it open (in sky or not)
}

// HistoryOptions say how much to list.
type HistoryOptions struct {
	Limit int // conversations typed by hand, per machine (0: 200)
	Auto  int // ones started by scripts, per machine (0: 100, negative: none)
	// ID lists only conversations whose ID starts with this.
	ID string
	// Sessions are the live sessions, to mark the conversations open in one right now. Nil
	// means not known: they are looked up.
	Sessions []Session
}

// historyTimeout is how long one machine gets to list its conversations.
const historyTimeout = 20 * time.Second

// perlCommon is shared by the history and the search script: finding the files, and reading
// what the first and last 64 KB of one say about its conversation. Every record it prints
// starts with an "@@conv" line; the lines after it are a letter, a tab and a value:
// c the folder, b the branch, s the last time stamp, r "is running", t a title line,
// u a line the user typed, q the first queued prompt. Lines are cut short, never joined.
const perlCommon = `use strict; use warnings;
use Time::HiRes qw(time);
$| = 1;
my $root = $ENV{CLAUDE_CONFIG_DIR} || "$ENV{HOME}/.claude";
my $dir = "$root/projects";
my $W = 65536;
my $only = $ENV{SKYID} // '';

# Every conversation file, newest first: [mtime, size, folder, id].
sub files {
  my @f;
  opendir(my $dh, $dir) or return @f;
  for my $p (readdir $dh) {
    next if $p =~ /^\.|[\t\r\n]/;
    opendir(my $ph, "$dir/$p") or next;
    for my $n (readdir $ph) {
      next unless $n =~ /^([A-Za-z0-9][A-Za-z0-9_-]{7,79})\.jsonl$/;
      my $id = $1;
      next if $only ne '' && index($id, $only) != 0;
      my @st = stat("$dir/$p/$n") or next;
      push @f, [$st[9], $st[7], $p, $id];
    }
    closedir $ph;
  }
  closedir $dh;
  return sort { $b->[0] <=> $a->[0] } @f;
}

# Conversations a claude process has open right now (Claude Code keeps a record per process).
my %running;
if (opendir(my $sh, "$root/sessions")) {
  for my $n (readdir $sh) {
    next unless $n =~ /^(\d+)\.json$/ && kill(0, $1);
    open(my $fh, '<', "$root/sessions/$n") or next;
    local $/; my $j = <$fh>; close $fh;
    $running{$1} = 1 if defined $j && $j =~ /"sessionId":"([A-Za-z0-9_-]+)"/;
  }
  closedir $sh;
}

sub lastm { my ($re, @b) = @_; for my $b (@b) { my $v; $v = $1 while $$b =~ /$re/g; return $v if defined $v } return undef }
sub firstm { my ($re, @b) = @_; for my $b (@b) { return $1 if $$b =~ /$re/ } return undef }

# How a conversation was started ("cli" by hand, "sdk-…" by a script), from the last lines of
# its file alone: cheap enough to ask of every file.
sub entry {
  my ($p, $id, $sz) = @_;
  open(my $fh, '<:raw', "$dir/$p/$id.jsonl") or return '';
  my $e = '';
  seek($fh, $sz - 8192, 0) if $sz > 8192;
  read($fh, $e, 8192);
  close $fh;
  return lastm(qr/"entrypoint":"([A-Za-z0-9_-]+)"/, \$e) // '';
}

# What the start and the end of a conversation's file say about it. Nothing in between is read.
sub meta {
  my ($mt, $sz, $p, $id) = @_;
  open(my $fh, '<:raw', "$dir/$p/$id.jsonl") or return undef;
  my ($h, $t) = ('', '');
  read($fh, $h, $W);
  if ($sz > $W) { seek($fh, ($sz > 2 * $W ? $sz - $W : $W), 0); read($fh, $t, $W); }
  close $fh;
  my %m = (mt => $mt, sz => $sz, p => $p, id => $id);
  $m{ep} = lastm(qr/"entrypoint":"([A-Za-z0-9_-]+)"/, \$t, \$h) // '';
  $m{cwd} = firstm(qr/"cwd":"((?:[^"\\]|\\.)*)"/, \$h, \$t);
  $m{br} = lastm(qr/"gitBranch":"((?:[^"\\]|\\.)*)"/, \$t, \$h);
  $m{ts} = lastm(qr/"timestamp":"([0-9T:.Z+-]+)"/, \$t, \$h);
  my %tl;
  for my $b (\$h, \$t) {
    while ($$b =~ /^(\{"type":"(custom-title|ai-title|summary|last-prompt)",.*)$/mg) {
      my ($l, $k) = ($1, $2);
      # Old versions wrote summaries of other conversations at the top of a file: only one
      # that points at a message of this file is about this conversation.
      if ($k eq 'summary') { next unless $l =~ /"leafUuid":"([^"]+)"/ && (index($h, "\"uuid\":\"$1\"") >= 0 || index($t, "\"uuid\":\"$1\"") >= 0) }
      $tl{$k} = substr($l, 0, 700);
    }
  }
  $m{tl} = [map { $tl{$_} } grep { defined $tl{$_} } qw(custom-title ai-title summary last-prompt)];
  my @u;
  unless ($tl{'custom-title'} || $tl{'ai-title'} || $tl{summary}) {
    # No title: the first things the user said, for one to be made from.
    while ($h =~ /^(.*"type":"user".*)$/mg) {
      my $l = $1;
      next if $l =~ /"tool_use_id"|"toolUseResult"|"isMeta":true|"isCompactSummary":true|"isSidechain":true/;
      next if $l =~ /"content":"<(?:command-name|command-message|local-command-|task-notification|bash-)/;   # not typed by the user
      push @u, "u\t" . substr($l, 0, 2500);
      last if @u >= 4;
    }
    push @u, "q\t" . substr($1, 0, 2500) if $h =~ /^(\{"type":"queue-operation","operation":"enqueue".*)$/m;
  }
  $m{u} = \@u;
  return \%m;
}

sub emit {
  my ($m) = @_;
  my $o = join("\t", '@@conv', $m->{p}, $m->{id}, $m->{mt}, $m->{sz}, $m->{ep}) . "\n";
  $o .= "c\t$m->{cwd}\n" if defined $m->{cwd};
  $o .= "b\t$m->{br}\n" if defined $m->{br} && $m->{br} ne '';
  $o .= "s\t$m->{ts}\n" if defined $m->{ts};
  $o .= "r\t1\n" if $running{$m->{id}};
  $o .= "t\t$_\n" for @{$m->{tl}};
  $o .= "$_\n" for @{$m->{u}};
  return $o;
}
`

// perlHistory lists conversations newest first until it has enough of each kind, then counts
// messages in the smaller ones for as long as that stays cheap ("@@n" lines).
const perlHistory = `
my ($L, $LA, $B) = ($ENV{SKYLIM} // 200, $ENV{SKYAUTO} // 100, ($ENV{SKYBUDGET} // 300) / 1000);
my ($ni, $na, $seen, @count) = (0, 0, 0);
my @all = files();
for my $f (@all) {
  last if $ni >= $L && $na >= $LA;
  $seen++;
  if ($ni >= $L || $na >= $LA) {
    # One kind is full: the end of the file says which kind this one is, before more is read.
    my $e = entry($f->[2], $f->[3], $f->[1]);
    next if $e ne '' && ($e =~ /^sdk/ ? $na >= $LA : $ni >= $L);
  }
  my $m = meta(@$f) or next;
  next unless defined $m->{cwd} && (@{$m->{tl}} || @{$m->{u}});   # nothing was ever said in it
  if ($m->{ep} =~ /^sdk/) { next if $na >= $LA; $na++ } else { next if $ni >= $L; $ni++; push @count, $f if $f->[1] <= 8000000 }
  print emit($m);
}
print join("\t", '@@end', scalar(@all), $seen) . "\n";
my $t0 = time;
for my $f (@count) {
  last if time - $t0 > $B;
  open(my $fh, '<:raw', "$dir/$f->[2]/$f->[3].jsonl") or next;
  my $n = 0;
  while (<$fh>) {
    if (index($_, '"role":"user"') >= 0) { $n++ if index($_, '"type":"user"') >= 0 && index($_, '"tool_use_id"') < 0 && index($_, '"isMeta":true') < 0 && index($_, '"isCompactSummary":true') < 0 && $_ !~ /"content":"<(?:command-name|command-message|local-command-|task-notification|bash-)/ }
    elsif (index($_, '"role":"assistant"') >= 0) { $n++ if index($_, '"type":"text"') >= 0 }
  }
  close $fh;
  print "\@\@n\t$f->[3]\t$n\n";
}
`

// perlScript wraps a perl program for a POSIX shell: the settings go in the environment and
// the program in on standard input, so nothing in it needs quoting. A machine without perl
// says so.
func perlScript(env map[string]string, program string) string {
	var b strings.Builder
	b.WriteString("command -v perl >/dev/null 2>&1 || { echo '@@noperl'; exit 0; }\n")
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString("export " + k + "=" + sshx.Quote(env[k]) + "\n")
	}
	b.WriteString("exec perl - <<'SKYPERL'\n" + program + "\nSKYPERL\n")
	return b.String()
}

// errNoPerl: the scripts that read Claude's history are perl, which every Mac and every
// Linux distribution sky sets up has.
var errNoPerl = errors.New("perl isn't installed there, and reading Claude's history needs it")

// streamOn runs a POSIX sh script on a machine (or on this computer, with `tmux` meaning
// sky's own server) and hands over each line of output as it arrives. On a machine the
// lines of standard error are mixed in.
func (e *Engine) streamOn(ctx context.Context, machine, script string, fn func(string)) error {
	if !IsLocal(machine) {
		m, err := e.Machine(machine)
		if err != nil {
			return err
		}
		// The login shell there may be zsh or fish: it only gets to start sh, which reads the
		// script from standard input. (A script of some size as an argument fails now and then
		// on a shared ssh connection: the request no longer fits the socket it is sent over.)
		return sshx.StreamInput(ctx, e.Target(m), "sh -s", strings.NewReader(script), fn)
	}
	if runtime.GOOS == "windows" {
		return errors.New("not available on Windows yet")
	}
	pre, err := localPrelude()
	if err != nil {
		pre = "tmux() { return 1; }; " // no tmux here: no sessions, but there may be conversations
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", pre+script)
	cmd.Dir = paths.Home()
	cmd.WaitDelay = 2 * time.Second // a child that outlives a cancelled script must not hold us
	var errb bytes.Buffer
	cmd.Stderr = &errb
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		fn(sc.Text())
	}
	_, _ = io.Copy(io.Discard, out) // a line too long to scan: let the script finish anyway
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			return errors.New(lastLines(msg, 3))
		}
		return err
	}
	return nil
}

// shOn is streamOn for a script whose output is wanted whole. On a machine a failure says
// what ssh said.
func (e *Engine) shOn(ctx context.Context, machine, script string) (string, error) {
	if IsLocal(machine) {
		var b strings.Builder
		err := e.streamOn(ctx, machine, script, func(line string) {
			b.WriteString(line)
			b.WriteByte('\n')
		})
		return b.String(), err
	}
	m, err := e.Machine(machine)
	if err != nil {
		return "", err
	}
	return sshx.RunInput(ctx, e.Target(m), "sh -s", strings.NewReader(script))
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// History lists the past conversations on one machine (LocalMachine: this computer), the
// most recent first.
func (e *Engine) History(ctx context.Context, machine string, o HistoryOptions) ([]Conversation, error) {
	ctx, cancel := context.WithTimeout(ctx, historyTimeout)
	defer cancel()
	limit, auto := o.Limit, o.Auto
	if limit <= 0 {
		limit = 200
	}
	if auto == 0 {
		auto = 100
	} else if auto < 0 {
		auto = 0
	}
	env := map[string]string{"SKYLIM": strconv.Itoa(limit), "SKYAUTO": strconv.Itoa(auto)}
	if o.ID != "" {
		if !conversationPrefix(o.ID) {
			return nil, errors.New("that isn't a conversation ID")
		}
		env["SKYID"] = o.ID
	}
	// The sessions are asked for alongside, when nobody handed them over.
	sessions := o.Sessions
	var wg sync.WaitGroup
	if sessions == nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if IsLocal(machine) {
				sessions, _ = e.LocalSessions(ctx)
			} else {
				sessions, _ = e.Sessions(ctx, machine)
			}
		}()
	}
	out, err := e.shOn(ctx, machine, perlScript(env, perlCommon+perlHistory))
	wg.Wait()
	var p historyParser
	for _, line := range strings.Split(out, "\n") {
		p.line(line)
	}
	if p.noPerl {
		return nil, errNoPerl
	}
	list := p.conversations(machine)
	if err != nil && len(list) == 0 {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errors.New("timed out")
		}
		return nil, err
	}
	markLive(list, sessions)
	return list, nil
}

// AllHistory lists the past conversations on this computer and on every machine that is up,
// in parallel, the most recent first. Machines that didn't answer are named in errs.
func (e *Engine) AllHistory(ctx context.Context, o HistoryOptions) ([]Conversation, map[string]string) {
	names := e.historyMachines()
	var mu sync.Mutex
	var out []Conversation
	errs := map[string]string{}
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			list, err := e.History(ctx, name, o)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs[name] = err.Error()
				return
			}
			out = append(out, list...)
		}()
	}
	wg.Wait()
	sortConversations(out)
	return out, errs
}

// StartSession makes a session the way opening it in a terminal would (in its folder, with
// Claude Code picking a conversation up again when asked), without attaching to it.
func (e *Engine) StartSession(ctx context.Context, machine, session string, o AttachOptions) error {
	if session == "" {
		return errors.New("a session name is needed")
	}
	if !IsLocal(machine) {
		m, err := e.Machine(machine)
		if err != nil {
			return err
		}
		if err := e.Ready(m); err != nil {
			return err
		}
	} else if LocalTmux() == "" {
		return ErrNoLocalTmux
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// The attach script with nothing to attach: `true` takes the place of tmux at its end.
	_, err := e.shOn(ctx, machine, attachScriptWith("true", session, o))
	return err
}

// historyMachines is where conversations can be: this computer, then every machine that
// isn't known to be off.
func (e *Engine) historyMachines() []string {
	names := []string{LocalMachine}
	all, _ := e.Machines()
	for _, m := range all {
		if m.Status == model.StatusStopped || m.Status == model.StatusMissing {
			continue
		}
		names = append(names, m.Name)
	}
	return names
}

func sortConversations(list []Conversation) {
	sort.SliceStable(list, func(i, j int) bool { return list[i].Updated.After(list[j].Updated) })
}

// conversationPrefix reports whether s could be the start of a conversation ID.
func conversationPrefix(s string) bool {
	if s == "" || len(s) > 80 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// markLive notes on each conversation the session it is open in right now, when there is one.
func markLive(list []Conversation, sessions []Session) {
	live := map[string]string{}
	for _, s := range sessions {
		if s.SID != "" && s.Claude && s.State != "" {
			live[s.Machine+"\n"+s.SID] = s.Name
		}
	}
	for i := range list {
		list[i].Live = live[list[i].Machine+"\n"+list[i].ID]
	}
}

// ---------- reading what the script prints ----------

// convRecord is what the script found about one conversation, as it printed it.
type convRecord struct {
	folder, id, entry  string
	mtime, size        int64
	cwd, branch, stamp string
	running            bool
	titles             []string // title lines of the transcript, most trusted first
	prompts            []string // the first lines the user typed
	queued             string   // the first prompt a script queued
}

// parseConvHeader reads an "@@conv" line.
func parseConvHeader(line string) (convRecord, bool) {
	f := strings.Split(line, "\t")
	if len(f) < 6 || f[0] != "@@conv" || !sessionID.MatchString(f[2]) {
		return convRecord{}, false
	}
	r := convRecord{folder: f[1], id: f[2], entry: f[5]}
	r.mtime, _ = strconv.ParseInt(f[3], 10, 64)
	r.size, _ = strconv.ParseInt(f[4], 10, 64)
	return r, true
}

// add takes one of the lines that follow an "@@conv" line; false when it isn't one.
func (r *convRecord) add(line string) bool {
	if len(line) < 2 || line[1] != '\t' {
		return false
	}
	v := line[2:]
	switch line[0] {
	case 'c':
		r.cwd = jsonText(v)
	case 'b':
		r.branch = jsonText(v)
	case 's':
		r.stamp = v
	case 'r':
		r.running = true
	case 't':
		r.titles = append(r.titles, v)
	case 'u':
		r.prompts = append(r.prompts, v)
	case 'q':
		r.queued = v
	default:
		return false
	}
	return true
}

func (r *convRecord) conversation(machine string) Conversation {
	c := Conversation{
		Machine: machine, ID: r.id, Dir: projectDir(r.folder, r.cwd), Title: r.title(), Size: r.size,
		Auto: strings.HasPrefix(r.entry, "sdk"), Running: r.running,
	}
	if r.branch != "HEAD" {
		c.Branch = r.branch
	}
	if t, err := time.Parse(time.RFC3339, r.stamp); err == nil {
		c.Updated = t
	} else if r.mtime > 0 {
		c.Updated = time.Unix(r.mtime, 0)
	}
	return c
}

// title picks what to call the conversation: the name the user gave it, the one Claude gave
// it, a summary of it, else the first thing the user really said, else the last.
func (r *convRecord) title() string {
	var last string
	for _, key := range []string{"customTitle", "aiTitle", "summary", "lastPrompt"} {
		for _, line := range r.titles {
			v, ok := jsonField(line, key)
			if !ok {
				continue
			}
			if key == "lastPrompt" {
				last = cleanPrompt(v)
			} else if t := oneLine(v, titleLen); t != "" {
				return t
			}
		}
	}
	for _, line := range r.prompts {
		if t := cleanPrompt(promptText(line)); t != "" {
			return t
		}
	}
	if v, ok := jsonField(r.queued, "content"); ok {
		if t := cleanPrompt(v); t != "" {
			return t
		}
	}
	return last
}

// historyParser collects the conversations of a history listing, line by line.
type historyParser struct {
	recs   []convRecord
	counts map[string]int
	noPerl bool
}

func (p *historyParser) line(line string) {
	line = strings.TrimRight(line, "\r")
	switch {
	case strings.HasPrefix(line, "@@conv\t"):
		if r, ok := parseConvHeader(line); ok {
			p.recs = append(p.recs, r)
		}
	case strings.HasPrefix(line, "@@n\t"):
		if f := strings.Split(line, "\t"); len(f) == 3 {
			if n, err := strconv.Atoi(f[2]); err == nil {
				if p.counts == nil {
					p.counts = map[string]int{}
				}
				p.counts[f[1]] = n
			}
		}
	case line == "@@noperl":
		p.noPerl = true
	case strings.HasPrefix(line, "@@"):
	case len(p.recs) > 0:
		p.recs[len(p.recs)-1].add(line)
	}
}

func (p *historyParser) conversations(machine string) []Conversation {
	list := make([]Conversation, 0, len(p.recs))
	seen := map[string]bool{}
	for i := range p.recs {
		c := p.recs[i].conversation(machine)
		if seen[c.ID] || c.Title == "" { // no title: nothing was really said in it
			continue
		}
		seen[c.ID] = true
		c.Messages = p.counts[c.ID]
		list = append(list, c)
	}
	sortConversations(list)
	return list
}

// parseHistory reads a whole history listing.
func parseHistory(machine, out string) []Conversation {
	var p historyParser
	for _, line := range strings.Split(out, "\n") {
		p.line(line)
	}
	return p.conversations(machine)
}

// ---------- what a transcript line says ----------

const titleLen = 140

// promptText is what the user typed in a "user" line of a transcript: the text of the
// message, or of its first text block. The line may be cut short (the script sends only the
// start of long ones), so when it doesn't parse the text is dug out of what is there. Lines
// that aren't something the user typed (tool results, notes Claude Code adds) give "".
func promptText(line string) string {
	var l struct {
		Type             string `json:"type"`
		IsMeta           bool   `json:"isMeta"`
		IsSidechain      bool   `json:"isSidechain"`
		IsCompactSummary bool   `json:"isCompactSummary"`
		Message          struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal([]byte(line), &l) == nil {
		if l.Type != "user" || l.Message.Role != "user" || l.IsMeta || l.IsSidechain || l.IsCompactSummary {
			return ""
		}
		var s string
		if json.Unmarshal(l.Message.Content, &s) == nil {
			return s
		}
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(l.Message.Content, &blocks) != nil {
			return ""
		}
		text := ""
		for _, b := range blocks {
			if b.Type == "tool_result" {
				return ""
			}
			if b.Type == "text" && text == "" {
				text = b.Text
			}
		}
		return text
	}
	// Cut short, or not JSON at all.
	const lead = `"role":"user","content":`
	i := strings.Index(line, lead)
	if i < 0 || !strings.Contains(line[:i], `"type":"user"`) {
		return ""
	}
	rest := line[i+len(lead):]
	switch {
	case strings.HasPrefix(rest, `"`):
		raw, _ := jsonStringAt(rest, 0)
		return jsonText(raw)
	case strings.HasPrefix(rest, "["):
		const block = `"type":"text","text":"`
		j := strings.Index(rest, block)
		if j < 0 || strings.Contains(rest[:j], `"tool_result"`) {
			return ""
		}
		raw, _ := jsonStringAt(rest, j+len(block)-1)
		return jsonText(raw)
	}
	return ""
}

// jsonField finds "key":"value" in a JSON line that may be cut short and returns the value.
// (Inside a JSON string every quote is escaped, so a bare "key":" can only be a real key.)
func jsonField(line, key string) (string, bool) {
	lead := `"` + key + `":"`
	i := strings.Index(line, lead)
	if i < 0 {
		return "", false
	}
	raw, _ := jsonStringAt(line, i+len(lead)-1)
	return jsonText(raw), true
}

// jsonStringAt returns the inside of the JSON string whose opening quote is at s[i], still
// escaped, and whether its closing quote was there (it isn't when the line was cut).
func jsonStringAt(s string, i int) (string, bool) {
	if i >= len(s) || s[i] != '"' {
		return "", false
	}
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '"':
			return s[i+1 : j], true
		}
	}
	return s[i+1:], false
}

// jsonText undoes JSON string escapes in text that may be cut anywhere: an escape broken by
// the cut is dropped, and so are bytes that aren't UTF-8 any more.
func jsonText(raw string) string {
	if !strings.Contains(raw, `\`) {
		return strings.ToValidUTF8(raw, "")
	}
	var b strings.Builder
	b.Grow(len(raw))
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(raw) {
			break
		}
		switch raw[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'b', 'f':
			b.WriteByte(' ')
		case 'u':
			r, n := jsonRune(raw[i+1:])
			if n == 0 {
				return strings.ToValidUTF8(b.String(), "") // cut inside the escape
			}
			b.WriteRune(r)
			i += n
		default:
			b.WriteByte(raw[i])
		}
	}
	return strings.ToValidUTF8(b.String(), "")
}

// jsonRune reads the "XXXX" of a \uXXXX escape (and the second half of a surrogate pair);
// it returns the character and how many bytes it took, 0 when they aren't all there.
func jsonRune(s string) (rune, int) {
	hex := func(s string) (rune, bool) {
		if len(s) < 4 {
			return 0, false
		}
		v, err := strconv.ParseUint(s[:4], 16, 16)
		return rune(v), err == nil
	}
	r, ok := hex(s)
	if !ok {
		return 0, 0
	}
	if r >= 0xD800 && r < 0xDC00 { // the first half of a pair
		if len(s) >= 10 && s[4] == '\\' && s[5] == 'u' {
			if lo, ok := hex(s[6:]); ok && lo >= 0xDC00 && lo < 0xE000 {
				return 0x10000 + (r-0xD800)<<10 + (lo - 0xDC00), 10
			}
		}
		return utf8.RuneError, 4
	}
	if r >= 0xDC00 && r < 0xE000 {
		return utf8.RuneError, 4
	}
	return r, 4
}

// Things Claude Code wraps around what the user typed: they are taken off.
var promptWrappers = []string{"system-reminder", "local-command-caveat", "ide_opened_file", "ide_selection", "ide_diagnostics", "user-prompt-submit-hook"}

// Things it writes in the user's name: nobody typed them.
var promptNoise = []string{"<task-notification", "<local-command-", "<bash-", "<user-memory-input", "<command-stdout", "<command-stderr", "<tool_use_error", "<teammate-message",
	"Caveat: The messages below were generated", "[Request interrupted", "This session is being continued from a previous conversation"}

// machineTag: a message that opens with a tag like <task-notification> or
// <cross-session-message from="…"> was written by Claude Code, not typed.
var machineTag = regexp.MustCompile(`^<[a-z][a-z0-9]*[-_][a-z0-9_-]*[\s>]`)

// cleanPrompt turns what a "user" line holds into what the user said, on one line and not
// too long; "" when the user didn't really say it (a command's output, a notification, a
// bare slash command).
func cleanPrompt(s string) string {
	s = strings.TrimSpace(s)
	for again := true; again; {
		again = false
		for _, tag := range promptWrappers {
			if !strings.HasPrefix(s, "<"+tag+">") {
				continue
			}
			end := strings.Index(s, "</"+tag+">")
			if end < 0 {
				return "" // cut inside it: what was typed comes after
			}
			s = strings.TrimSpace(s[end+len(tag)+3:])
			again = true
		}
	}
	if strings.HasPrefix(s, "<command-name>") || strings.HasPrefix(s, "<command-message>") {
		// A slash command says something about the conversation only with what it was given.
		name, args := between(s, "<command-name>", "</command-name>"), between(s, "<command-args>", "</command-args>")
		if name == "" || args == "" {
			return ""
		}
		if !strings.HasPrefix(name, "/") {
			name = "/" + name
		}
		s = name + " " + args
	}
	for _, n := range promptNoise {
		if strings.HasPrefix(s, n) {
			return ""
		}
	}
	if machineTag.MatchString(s) {
		return ""
	}
	return oneLine(s, titleLen)
}

func between(s, open, close string) string {
	i := strings.Index(s, open)
	if i < 0 {
		return ""
	}
	s = s[i+len(open):]
	j := strings.Index(s, close)
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(s[:j])
}

// oneLine puts text on one line (runs of white space become one space) and cuts it to max
// characters, with an ellipsis when something was left out.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return strings.TrimRight(string(r[:max-1]), " ") + "…"
}

// encodeProject is how Claude Code names a folder's directory of transcripts: every
// character that isn't a letter or a digit becomes a dash.
func encodeProject(dir string) string {
	b := []byte(dir)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			b[i] = '-'
		}
	}
	return string(b)
}

// projectDir is the folder a conversation has to be resumed in: the one its transcripts
// folder is named after. The transcript's first line usually says it, but a conversation
// carried on after a `cd` starts in a folder below: then it is the parent that matches.
func projectDir(folder, cwd string) string {
	if cwd == "" || folder == "" {
		return cwd
	}
	for d := cwd; len(d) > 1; d = path.Dir(d) {
		if encodeProject(d) == folder {
			return d
		}
		if path.Dir(d) == d {
			break
		}
	}
	return cwd
}
