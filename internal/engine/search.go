package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"skybuild/internal/sshx"
)

// Search everything: what is on the screen and in the scrollback of every live session, and
// what was said in every past conversation, on every machine and on this computer.
//
// Each machine runs one script. The first part asks tmux for the last few thousand lines of
// every session and keeps the lines that match. The second part is perl going through the
// transcripts newest first: it stops at enough conversations or when its time is up, and it
// only counts a match inside what the user typed or Claude answered (not in a tool's output,
// a file Claude read, or the bookkeeping around a message). Results are handed over as they
// are found, so the newest show at once even where there are gigabytes to go through.

// SearchHit is one place the words were found.
type SearchHit struct {
	Kind    string    `json:"kind"`              // "live": in a session's screen or scrollback; "past": in a conversation
	Machine string    `json:"machine"`           // LocalMachine for this computer
	Session string    `json:"session,omitempty"` // live: the tmux session
	ID      string    `json:"id,omitempty"`      // past: the conversation (live: the one Claude has open there, when known)
	Dir     string    `json:"dir,omitempty"`     // the folder the session is in, or the conversation ran in
	Title   string    `json:"title,omitempty"`   // what the session or the conversation is called
	Claude  bool      `json:"claude,omitempty"`  // live: Claude Code runs in the session
	Role    string    `json:"role,omitempty"`    // past: who said it (user, assistant), or "title" when its name matched
	At      time.Time `json:"at"`                // past: when it was said; live: the session's last output
	Updated time.Time `json:"updated"`           // past: the last thing said in the conversation
	Snippet string    `json:"snippet"`           // the text around the match, on one line
	Before  string    `json:"before,omitempty"`  // live: the line above it
	After   string    `json:"after,omitempty"`   // live: the line below it
	Matches int       `json:"matches,omitempty"` // live: how many lines of the session match
	Auto    bool      `json:"auto,omitempty"`    // past: a conversation started by a script
	Live    string    `json:"live,omitempty"`    // past: the session the conversation is open in right now
}

// SearchPart is news from one machine while a search runs: more hits, and at the end how it
// went.
type SearchPart struct {
	Machine string      `json:"machine"`
	Hits    []SearchHit `json:"hits"`
	Done    bool        `json:"done"`              // the machine is through
	Error   string      `json:"error,omitempty"`   // it didn't answer, or not all the way
	Partial bool        `json:"partial,omitempty"` // time ran out before the oldest conversations
	Scanned int         `json:"scanned"`           // conversations gone through so far
	Total   int         `json:"total"`             // conversations there
}

// SearchResult is a finished search.
type SearchResult struct {
	Query    string            `json:"query"`
	Hits     []SearchHit       `json:"hits"`     // open sessions first, then conversations, newest first
	Machines []string          `json:"machines"` // where it looked
	Errors   map[string]string `json:"errors"`   // machines that didn't answer (all the way)
	Scanned  int               `json:"scanned"`  // conversations gone through
	Total    int               `json:"total"`    // conversations there are
}

// SearchOptions narrow or follow a search.
type SearchOptions struct {
	Machines []string      // where to look (LocalMachine: this computer); empty: everywhere
	Limit    int           // conversations with a match, per machine (0: 40)
	Auto     int           // of which started by scripts, on top (0: 10, negative: none)
	Timeout  time.Duration // what one machine gets (0: 12s)
	// Sessions are the live sessions when the caller has them: they name the sessions found and
	// mark the conversations open right now. Nil: they are looked up.
	Sessions []Session
	// Started is called once, before anything else, with the machines it looks on.
	Started func(machines []string)
	// Each is called with hits as they arrive and once more per machine when it is through.
	// Calls never overlap.
	Each func(SearchPart)
}

// SearchMin is the shortest query worth running: one letter matches everything.
const SearchMin = 2

// scrollback is how many lines of a session's history are searched.
const scrollback = 5000

// liveSearch prints, for every tmux session, a "@@live" line (name, folder, last output,
// host, title), then how many of its lines match and the last three with the line above and
// below each. SKYQ is the query in lower case; only ASCII letters match in either case.
var liveSearch = `tmux list-sessions -F '#{session_name}` + "\t" + `#{pane_current_path}` + "\t" + `#{window_activity}` + "\t" + `#{host_short}` + "\t" + `#{pane_title}' 2>/dev/null | while IFS='` + "\t" + `' read -r s p act h t; do
  printf '@@live\t%s\t%s\t%s\t%s\t%s\n' "$s" "$p" "$act" "$h" "$t"
  tmux capture-pane -p -J -S -` + strconv.Itoa(scrollback) + ` -t "=$s:" 2>/dev/null </dev/null | LC_ALL=C awk '
    BEGIN { q = ENVIRON["SKYQ"] }
    {
      if (want && $0 ~ /[^ \t]/) { nx[want] = substr($0, 1, 300); want = 0 }
      p = index(tolower($0), q)
      if (p) { n++; ln[n] = NR; tx[n] = substr($0, (p > 300 ? p - 300 : 1), 600 + length(q)); pv[n] = prev; want = n }
      if ($0 ~ /[^ \t]/) prev = substr($0, 1, 300)
    }
    END {
      printf "@@count\t%d\t%d\n", n, NR
      for (i = n; i > 0 && i > n - 3; i--) { printf "@@m\t%d\n", ln[i]; print "<" pv[i]; print "=" tx[i]; print ">" nx[i] }
    }'
done
echo '@@livedone'
`

// perlSearch goes through the transcripts newest first. For a conversation with a match it
// prints the record perlCommon describes, then "@@hit" lines: who said it, when, whether the
// text was cut on the left and on the right, and the text before, of and after the match
// (still escaped as in the file), then "@@." to close the record. SKYQ is the query as JSON
// writes it. "@@prog" lines say how far it is; "@@done" ends it, with "time" when the budget
// ran out first.
const perlSearch = `
use IO::Select;
my $q = $ENV{SKYQ} // '';
exit 0 if $q eq '';
my $re = qr/\Q$q\E/i;
my ($CAP, $CAPA, $PER, $B) = ($ENV{SKYLIM} // 40, $ENV{SKYAUTO} // 10, $ENV{SKYPER} // 2, ($ENV{SKYBUDGET} // 8000) / 1000);
my ($J, $JMIN) = ($ENV{SKYJOBS} // 4, $ENV{SKYJOBSMIN} // 200);
my @all = files();
my ($n, $na, $seen, $t0, $why) = (0, 0, 0, time, '');
my $tick = $t0;

sub isauto { my ($f) = @_; return entry($f->[2], $f->[3], $f->[1]) =~ /^sdk/ ? 1 : 0 }

# The record of one conversation with its hits; '' when the words aren't in what was said.
sub scan {
  my ($f) = @_;
  open(my $fh, '<:raw', "$dir/$f->[2]/$f->[3].jsonl") or return '';
  my ($k, $out, $title) = (0, '', undef);
  while (my $l = <$fh>) {
    next unless $l =~ $re;
    if ($l =~ /^\{"type":"(?:custom-title|ai-title)","(?:customTitle|aiTitle)":"((?:[^"\\]|\\.)*)"/) { my $t = $1; $title = $t if $t =~ $re; next }
    next if index($l, '"tool_use_id"') >= 0 || index($l, '"isMeta":true') >= 0 || index($l, '"isCompactSummary":true') >= 0;
    my $role = index($l, '"role":"assistant"') >= 0 && index($l, '"type":"assistant"') >= 0 ? 'assistant' : index($l, '"role":"user"') >= 0 && index($l, '"type":"user"') >= 0 ? 'user' : '';
    next unless $role;
    my @hit;
    while (!@hit && $l =~ /(?:"role":"user","content":|"type":"text","text":)"((?:[^"\\]++|\\.)*+)"/g) {
      my $s = $1;
      next if $role eq 'user' && $s =~ /^<(?:task-notification|local-command-|command-name|command-message|system-reminder|bash-)/;
      while ($s =~ /$re/g) {
        my ($a, $b) = ($-[0], $+[0]);
        my $bs = 0;
        $bs++ while $a - $bs > 0 && substr($s, $a - $bs - 1, 1) eq '\\';
        next if $bs % 2;   # it starts inside an escape (the n of \n): not what was typed
        my $from = $a > 240 ? $a - 240 : 0;
        @hit = (($from > 0 ? 1 : 0) . (length($s) - $b > 240 ? 1 : 0), substr($s, $from, $a - $from), substr($s, $a, $b - $a), substr($s, $b, 240));
        last;
      }
    }
    next unless @hit;
    if ($out eq '') { my $m = meta(@$f) or last; $out = emit($m) }
    my $ts = '';
    my $i = rindex($l, '"timestamp":"');
    $ts = $1 if $i >= 0 && substr($l, $i, 60) =~ /"timestamp":"([0-9T:.Z+-]+)"/;
    $out .= join("\t", '@@hit', $role, $ts, @hit) . "\n";
    last if ++$k >= $PER;
  }
  close $fh;
  if ($out eq '' && defined $title) { my $m = meta(@$f); $out = emit($m) . join("\t", '@@hit', 'title', '', '00', '', $title, '') . "\n" if $m }
  return $out;
}

if (@all < $JMIN || $J < 2) {
  for my $f (@all) {
    if ($n >= $CAP) { $why = 'cap'; last }
    if (time - $t0 > $B) { $why = 'time'; last }
    if (time - $tick > 0.2) { $tick = time; print join("\t", '@@prog', $seen, scalar(@all)) . "\n" }
    $seen++;
    # Runs started by scripts are many and alike: a few of them are enough, the rest is skipped.
    my $auto = isauto($f);
    next if $auto && $na >= $CAPA;
    my $out = scan($f);
    if ($out ne '') { $auto ? $na++ : $n++; print $out . "@@.\n" }
  }
} else {
  # Many files: a few workers go through them at once, each taking every $J-th file, and tell
  # the parent about every file they finish. The parent hands the records over in the order
  # of the files, so the result is what one worker alone would have printed.
  my $sel = IO::Select->new;
  my (@kids, %buf);
  for my $w (0 .. $J - 1) {
    pipe(my $r, my $wr) or die "pipe: $!";
    my $pid = fork();
    die "fork: $!" unless defined $pid;
    if ($pid == 0) {
      close $r;
      select((select($wr), $| = 1)[0]);
      my ($k, $ka) = (0, 0);
      for (my $i = $w; $i < @all; $i += $J) {
        my $f = $all[$i];
        my $auto = isauto($f);
        my $out = ($auto ? $ka >= $CAPA : $k >= $CAP) ? '' : scan($f);
        if ($out eq '') { print $wr "D\t$i\n"; next }
        $auto ? $ka++ : $k++;
        $out =~ s/\n/\x01/g;
        print $wr "H\t$i\t$auto\t$out\n";
      }
      exit 0;
    }
    close $wr;
    push @kids, $pid;
    $sel->add($r);
    $buf{fileno($r)} = '';
  }
  my %st;   # file number: its record, or '' when it has none
  my ($next, $open) = (0, scalar(@kids));
  while ($open > 0 && $why eq '') {
    for my $r ($sel->can_read(0.1)) {
      my $fn = fileno($r);
      my $chunk = '';
      if (!sysread($r, $chunk, 65536)) { $sel->remove($r); close $r; $open--; next }
      $buf{$fn} .= $chunk;
      while ($buf{$fn} =~ s/^([^\n]*)\n//) {
        my @p = split(/\t/, $1, 4);
        $st{$p[1]} = $p[0] eq 'H' ? [$p[2], $p[3]] : '';
      }
    }
    while (exists $st{$next}) {
      my $s = delete $st{$next};
      $next++; $seen++;
      next unless ref $s;
      my ($auto, $out) = @$s;
      if ($auto) { next if $na >= $CAPA; $na++ } else { $n++ }
      $out =~ s/\x01/\n/g;
      print $out . "@@.\n";
      if ($n >= $CAP) { $why = 'cap'; last }
    }
    $why = 'time' if $why eq '' && time - $t0 > $B;
    if (time - $tick > 0.2) { $tick = time; print join("\t", '@@prog', $seen, scalar(@all)) . "\n" }
  }
  kill 'TERM', @kids;
  waitpid($_, 0) for @kids;
  $why = '' if $seen >= @all;
}
print join("\t", '@@done', $seen, scalar(@all), $why) . "\n";
`

// searchJobsMin is how many conversations a machine needs before its search splits the work
// between several processes (below that one is as quick).
var searchJobsMin = 200

// searchScript is what one machine runs for a search.
func searchScript(query string, limit, auto int, budget time.Duration) string {
	env := map[string]string{
		"SKYQ":       jsonFragment(query),
		"SKYLIM":     strconv.Itoa(limit),
		"SKYAUTO":    strconv.Itoa(auto),
		"SKYBUDGET":  strconv.FormatInt(budget.Milliseconds(), 10),
		"SKYJOBSMIN": strconv.Itoa(searchJobsMin),
	}
	return "SKYQ=" + sshx.Quote(asciiLower(query)) + "; export SKYQ\n" + liveSearch + perlScript(env, perlCommon+perlSearch)
}

// Search looks for query in every live session and every past conversation, on every machine
// that is up and on this computer, all at once. It matches the words as typed, in either
// case. A machine that is slow or down doesn't hold the others: it is named in Errors.
func (e *Engine) Search(ctx context.Context, query string, o SearchOptions) SearchResult {
	query = strings.TrimSpace(query)
	res := SearchResult{Query: query, Hits: []SearchHit{}, Errors: map[string]string{}, Machines: o.Machines}
	if len(res.Machines) == 0 {
		res.Machines = e.historyMachines()
	}
	if o.Started != nil {
		o.Started(res.Machines)
	}
	if utf8.RuneCountInString(query) < SearchMin {
		return res
	}
	limit, auto, timeout := o.Limit, o.Auto, o.Timeout
	if limit <= 0 {
		limit = 40
	}
	if auto == 0 {
		auto = 10
	} else if auto < 0 {
		auto = 0
	}
	if timeout <= 0 {
		timeout = 12 * time.Second
	}
	// The script stops by itself a little before it would be cut off, and says how far it got.
	budget := timeout - 2*time.Second
	if budget < time.Second {
		budget = timeout / 2
	}
	script := searchScript(query, limit, auto, budget)

	var mu sync.Mutex // guards res and keeps calls to o.Each apart
	each := func(p SearchPart) {
		if o.Each != nil {
			o.Each(p)
		}
	}
	var wg sync.WaitGroup
	for _, machine := range res.Machines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			sessions := o.Sessions
			var sw sync.WaitGroup
			if sessions == nil {
				sw.Add(1)
				go func() {
					defer sw.Done()
					if IsLocal(machine) {
						sessions, _ = e.LocalSessions(c)
					} else {
						sessions, _ = e.Sessions(c, machine)
					}
				}()
			}
			var all []SearchHit
			p := searchParser{machine: machine, query: query}
			p.emit = func(hits []SearchHit) {
				mu.Lock()
				defer mu.Unlock()
				all = append(all, hits...)
				if hits == nil {
					hits = []SearchHit{} // only how far it is
				}
				each(SearchPart{Machine: machine, Hits: hits, Scanned: p.scanned, Total: p.total})
			}
			err := e.streamOn(c, machine, script, p.line)
			p.flush()
			sw.Wait()
			nameHits(all, sessions)

			end := SearchPart{Machine: machine, Hits: []SearchHit{}, Done: true, Scanned: p.scanned, Total: p.total, Partial: p.why == "time"}
			switch {
			case p.noPerl:
				end.Error = "past conversations weren't searched: " + errNoPerl.Error()
			case p.done:
			case ctx.Err() != nil:
				end.Error = "cancelled"
			case errors.Is(c.Err(), context.DeadlineExceeded):
				end.Error = "timed out"
			case err != nil:
				end.Error = err.Error()
				if len(p.junk) > 0 { // on a machine, what ssh said is in the output
					end.Error = lastLines(strings.Join(p.junk, "\n"), 2)
				}
			default:
				end.Error = "stopped before the end"
			}
			mu.Lock()
			defer mu.Unlock()
			res.Hits = append(res.Hits, all...)
			res.Scanned += p.scanned
			res.Total += p.total
			if end.Error != "" {
				res.Errors[machine] = end.Error
			} else if end.Partial {
				res.Errors[machine] = "ran out of time after " + strconv.Itoa(p.scanned) + " of " + strconv.Itoa(p.total) + " conversations"
			}
			each(end)
		}()
	}
	wg.Wait()
	res.Hits = mergeHits(res.Hits)
	return res
}

// ShowMatch scrolls a session to the latest place query shows in it: tmux's own copy mode,
// searching backwards from the bottom. The session stays in copy mode (q or Escape leaves it).
func (e *Engine) ShowMatch(ctx context.Context, machine, session, query string) error {
	query = strings.TrimSpace(query)
	if session == "" || query == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	t := sshx.Quote("=" + session + ":")
	// All lower case, so tmux ignores case as the search did. Older tmux has no -text variant;
	// there search-backward takes plain text.
	q := sshx.Quote(asciiLower(query))
	script := "tmux send-keys -t " + t + " -X cancel 2>/dev/null; tmux copy-mode -t " + t + " && { tmux send-keys -t " + t + " -X search-backward-text " + q + " 2>/dev/null || tmux send-keys -t " + t + " -X search-backward " + q + "; }"
	_, err := e.shOn(ctx, machine, script)
	return err
}

// ---------- reading what the script prints ----------

// searchParser turns the script's lines into hits, handing over each session's and each
// conversation's as soon as they are complete.
type searchParser struct {
	machine, query string
	emit           func([]SearchHit)

	live     *SearchHit // the session being read
	matches  []liveMatch
	conv     *convRecord // the conversation being read
	hits     []pastHit
	scanned  int
	total    int
	why      string
	done     bool
	noPerl   bool
	liveDone bool
	junk     []string // lines that aren't the script's: what ssh or the shell said
}

type liveMatch struct{ prev, text, next string }

type pastHit struct {
	role, stamp, cut     string
	before, match, after string
}

func (p *searchParser) line(line string) {
	line = strings.TrimRight(line, "\r")
	if p.live != nil && len(line) > 0 && len(p.matches) > 0 {
		m := &p.matches[len(p.matches)-1]
		switch line[0] {
		case '<':
			m.prev = line[1:]
			return
		case '=':
			m.text = line[1:]
			return
		case '>':
			m.next = line[1:]
			return
		}
	}
	f := strings.Split(line, "\t")
	switch f[0] {
	case "@@live":
		p.flush()
		if len(f) < 4 {
			return
		}
		h := SearchHit{Kind: "live", Machine: p.machine, Session: f[1], Dir: f[2]}
		if n, err := strconv.ParseInt(f[3], 10, 64); err == nil && n > 0 {
			h.At = time.Unix(n, 0)
		}
		if len(f) >= 6 {
			h.Title = cleanTitle(f[5], f[4])
		}
		p.live = &h
	case "@@count":
		if p.live != nil && len(f) >= 2 {
			p.live.Matches, _ = strconv.Atoi(f[1])
		}
	case "@@m":
		if p.live != nil {
			p.matches = append(p.matches, liveMatch{})
		}
	case "@@livedone":
		p.flush()
		p.liveDone = true
	case "@@conv":
		p.flush()
		if r, ok := parseConvHeader(line); ok {
			p.conv = &r
		}
	case "@@hit":
		if p.conv != nil && len(f) >= 7 {
			p.hits = append(p.hits, pastHit{role: f[1], stamp: f[2], cut: f[3], before: f[4], match: f[5], after: f[6]})
		}
	case "@@.":
		p.flush()
	case "@@prog", "@@done":
		p.flush()
		if len(f) >= 3 {
			p.scanned, _ = strconv.Atoi(f[1])
			p.total, _ = strconv.Atoi(f[2])
		}
		if f[0] == "@@done" {
			p.done = true
			if len(f) >= 4 {
				p.why = f[3]
			}
		} else if p.emit != nil {
			p.emit(nil) // how far it is
		}
	case "@@noperl":
		p.flush()
		p.noPerl = true
	default:
		if p.conv != nil && p.conv.add(line) {
			return
		}
		if !strings.HasPrefix(line, "@@") && strings.TrimSpace(line) != "" && len(p.junk) < 20 && !sshNoise(line) {
			p.junk = append(p.junk, line)
		}
	}
}

// sshNoise: lines ssh prints on every connection, which say nothing about a failure.
func sshNoise(line string) bool {
	return strings.Contains(line, "Warning: Permanently added") || strings.Contains(line, "post-quantum") || strings.Contains(line, "store now, decrypt later") || strings.Contains(line, "openssh.com/pq.html")
}

// flush hands over the session or conversation read so far.
func (p *searchParser) flush() {
	var out []SearchHit
	if p.live != nil {
		for _, m := range p.matches {
			h := *p.live
			h.Snippet = lineSnippet(strings.ToValidUTF8(m.text, ""), p.query)
			if h.Snippet == "" {
				continue
			}
			h.Before, h.After = oneLine(strings.ToValidUTF8(m.prev, ""), 160), oneLine(strings.ToValidUTF8(m.next, ""), 160)
			out = append(out, h)
		}
		p.live, p.matches = nil, nil
	}
	if p.conv != nil {
		c := p.conv.conversation(p.machine)
		for _, x := range p.hits {
			h := SearchHit{Kind: "past", Machine: p.machine, ID: c.ID, Dir: c.Dir, Title: c.Title, Role: x.role, At: c.Updated, Updated: c.Updated, Auto: c.Auto}
			if t, err := time.Parse(time.RFC3339, x.stamp); err == nil {
				h.At = t
			}
			h.Snippet = snippet(x.before, x.match, x.after, strings.HasPrefix(x.cut, "1"), strings.HasSuffix(x.cut, "1"))
			if h.Snippet != "" {
				out = append(out, h)
			}
		}
		p.conv, p.hits = nil, nil
	}
	if len(out) > 0 && p.emit != nil {
		p.emit(out)
	}
}

// nameHits fills in what the session list knows: what a live session is called and which
// conversation it has open, and which past conversations are open in a session right now.
func nameHits(hits []SearchHit, sessions []Session) {
	byName, bySID := map[string]Session{}, map[string]string{}
	for _, s := range sessions {
		byName[s.Machine+"\n"+s.Name] = s
		if s.SID != "" && s.Claude && s.State != "" {
			bySID[s.Machine+"\n"+s.SID] = s.Name
		}
	}
	for i := range hits {
		h := &hits[i]
		if h.Kind == "live" {
			if s, ok := byName[h.Machine+"\n"+h.Session]; ok {
				h.Claude = s.Claude
				if s.Title != "" {
					h.Title = s.Title
				}
				if s.Claude && s.State != "" {
					h.ID = s.SID
				}
			}
			continue
		}
		h.Live = bySID[h.Machine+"\n"+h.ID]
	}
}

// mergeHits puts the hits of every machine in the order they are shown: open sessions first
// (the one with the latest output on top), then conversations by when they were last active,
// the ones started by scripts last. Hits of one session or conversation stay together, in
// the order they came, and a hit that came twice is kept once.
func mergeHits(hits []SearchHit) []SearchHit {
	rank := func(h SearchHit) int {
		switch {
		case h.Kind == "live":
			return 0
		case !h.Auto:
			return 1
		}
		return 2
	}
	when := func(h SearchHit) time.Time {
		if h.Kind == "live" {
			return h.At
		}
		return h.Updated
	}
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if ra, rb := rank(a), rank(b); ra != rb {
			return ra < rb
		}
		if ta, tb := when(a), when(b); !ta.Equal(tb) {
			return ta.After(tb)
		}
		if a.Machine != b.Machine {
			return a.Machine < b.Machine
		}
		return a.Session+"\n"+a.ID < b.Session+"\n"+b.ID
	})
	out := hits[:0]
	seen := map[string]bool{}
	for _, h := range hits {
		k := h.Kind + "\n" + h.Machine + "\n" + h.Session + "\n" + h.ID + "\n" + h.Snippet
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, h)
	}
	return out
}

// ---------- snippets ----------

const (
	snippetBefore = 60  // characters shown before the match
	snippetAfter  = 150 // and after it
)

// snippet makes the line shown for a match in a transcript from the text before, of and after
// it as the script cut them out of the file: still JSON-escaped, and cut without regard for
// escapes or characters when cutLeft or cutRight say more text was there.
func snippet(before, match, after string, cutLeft, cutRight bool) string {
	// A cut on the left can split an escape or a character; what that leaves is at the very
	// start, which around drops (it keeps the end of the text before the match, from a word on).
	return around(jsonText(before), jsonText(match), jsonText(after), cutLeft, cutRight)
}

// lineSnippet is the same for a line of a terminal: the text around the first place the
// query shows in it. Empty when it doesn't.
func lineSnippet(line, query string) string {
	i := indexFold(line, query)
	if i < 0 {
		return ""
	}
	return around(line[:i], line[i:i+len(query)], line[i+len(query):], false, false)
}

// around puts a match and the text around it on one line: white space squeezed, a little
// before the match and more after, cut at words where it can be, with an ellipsis where text
// was left out.
func around(before, match, after string, cutLeft, cutRight bool) string {
	before, match, after = squeeze(before), squeeze(match), squeeze(after)
	if match == "" {
		return ""
	}
	if r := []rune(before); len(r) > snippetBefore || cutLeft {
		if len(r) > snippetBefore {
			r = r[len(r)-snippetBefore:]
		}
		// Start at a word, unless that leaves next to nothing.
		if i := indexRune(r, ' '); i >= 0 && i < len(r)-12 {
			r = r[i+1:]
		}
		before = "…" + string(r)
	} else {
		before = strings.TrimLeft(before, " ")
	}
	if r := []rune(after); len(r) > snippetAfter || cutRight {
		if len(r) > snippetAfter {
			r = r[:snippetAfter]
		}
		if i := lastIndexRune(r, ' '); i > 12 {
			r = r[:i]
		}
		after = strings.TrimRight(string(r), " ") + "…"
	} else {
		after = strings.TrimRight(after, " ")
	}
	return before + match + after
}

// squeeze makes every run of white space one space, keeping a space at either end (it joins
// the piece to its neighbour).
func squeeze(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			space = true
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	if space {
		b.WriteByte(' ')
	}
	return b.String()
}

func indexRune(r []rune, c rune) int {
	for i, x := range r {
		if x == c {
			return i
		}
	}
	return -1
}

func lastIndexRune(r []rune, c rune) int {
	for i := len(r) - 1; i >= 0; i-- {
		if r[i] == c {
			return i
		}
	}
	return -1
}

// asciiLower lowers A–Z and leaves every other byte alone: the scripts compare bytes, and
// only ASCII letters match in either case.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// indexFold finds sub in s the way the scripts do: ASCII letters in either case.
func indexFold(s, sub string) int {
	return strings.Index(asciiLower(s), asciiLower(sub))
}

// jsonFragment is s as it stands inside a JSON string in a transcript (Claude Code writes
// them with JavaScript's JSON.stringify: quotes, backslashes and control characters are
// escaped, everything else is as typed).
func jsonFragment(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if enc.Encode(s) != nil {
		return s
	}
	out := strings.TrimSpace(b.String())
	if len(out) < 2 {
		return s
	}
	return out[1 : len(out)-1]
}
