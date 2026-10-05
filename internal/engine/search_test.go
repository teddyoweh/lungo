package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSnippet(t *testing.T) {
	cases := []struct {
		name                 string
		before, match, after string
		cutL, cutR           bool
		want                 string
	}{
		{"short", "please rename the ", "Zephyr", " module", false, false, "please rename the Zephyr module"},
		{"escapes", `He said:\n\n\"`, `zephyr`, `\" is\tdone.\nNext`, false, false, `He said: "zephyr" is done. Next`},
		{"match at the start", "", "zephyr", " first", false, false, "zephyr first"},
		{"match at the end", "last ", "zephyr", "", false, false, "last zephyr"},
		{"unicode", `caf\u00e9 `, "zephyr", ` na\u00efve \ud83d\ude00`, false, false, "café zephyr naïve 😀"},
		// Cut by the script: an escape or a character split at the edge never shows.
		{"cut left in an escape", `n and then some words before the `, "zephyr", " after", true, false, "…and then some words before the zephyr after"},
		{"cut right in an escape", "before ", "zephyr", ` after words and more of them\`, false, true, "before zephyr after words and more of…"},
		{"cut in a character", "\xa9 start of it here ", "zephyr", " end \xc3", true, true, "…start of it here zephyr end…"},
		{"only space around", "\n\n", "zephyr", "\n", false, false, "zephyr"},
		{"nothing matched", "a", "", "b", false, false, ""},
	}
	for _, c := range cases {
		if got := snippet(c.before, c.match, c.after, c.cutL, c.cutR); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	// Long text on both sides: a little before, more after, cut at words.
	before := strings.Repeat("alpha beta gamma ", 30)
	after := strings.Repeat(" delta epsilon zeta", 30)
	got := snippet(before, "ZEPHYR", after, false, false)
	i := strings.Index(got, "ZEPHYR")
	if i < 0 || !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") {
		t.Fatalf("long: %q", got)
	}
	b, a := []rune(got[:i]), []rune(got[i+len("ZEPHYR"):])
	if len(b) > snippetBefore+1 || len(b) < snippetBefore-20 || len(a) > snippetAfter+1 || len(a) < snippetAfter-20 {
		t.Errorf("long: %d before and %d after: %q", len(b), len(a), got)
	}
	if !strings.HasPrefix(got, "…alpha ") && !strings.HasPrefix(got, "…beta ") && !strings.HasPrefix(got, "…gamma ") {
		t.Errorf("long: doesn't start at a word: %q", got)
	}
	if !strings.HasSuffix(got, "delta…") && !strings.HasSuffix(got, "epsilon…") && !strings.HasSuffix(got, "zeta…") {
		t.Errorf("long: doesn't end at a word: %q", got)
	}
	// One endless word: still cut to size.
	if got := snippet(strings.Repeat("x", 500), "zephyr", strings.Repeat("y", 500), true, true); len([]rune(got)) > snippetBefore+snippetAfter+8 {
		t.Errorf("endless word: %d characters", len([]rune(got)))
	}
}

func TestLineSnippet(t *testing.T) {
	if got := lineSnippet("  $ echo   ZEPHYR-Lantern-1  done ", "zephyr-lantern"); got != "$ echo ZEPHYR-Lantern-1 done" {
		t.Errorf("line: %q", got)
	}
	if got := lineSnippet("nothing here", "zephyr"); got != "" {
		t.Errorf("no match: %q", got)
	}
	long := strings.Repeat("left ", 100) + "zephyr" + strings.Repeat(" right", 100)
	if got := lineSnippet(long, "ZEPHYR"); !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") || !strings.Contains(got, "left zephyr right") {
		t.Errorf("long line: %q", got)
	}
	// Letters outside ASCII match as typed only, like the scripts' search.
	if lineSnippet("CAFÉ", "café") != "" || lineSnippet("café", "CAFé") == "" {
		t.Error("only ASCII letters match in either case")
	}
}

func TestJSONFragment(t *testing.T) {
	cases := map[string]string{
		`plain words`:   `plain words`,
		`say "hi"`:      `say \"hi\"`,
		`C:\temp`:       `C:\\temp`,
		`a<b> & c`:      `a<b> & c`,
		`tab	here`:      `tab\there`,
		`café 😀`:        `café 😀`,
		`it's`:          `it's`,
		"line\nbreak":   `line\nbreak`,
		"bell\x07":      `bell\u0007`,
		"":              "",
		`back\\slashes`: `back\\\\slashes`,
	}
	for in, want := range cases {
		if got := jsonFragment(in); got != want {
			t.Errorf("jsonFragment(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSearchParser(t *testing.T) {
	lines := []string{
		"** WARNING: connection is not using a post-quantum key exchange algorithm.",
		"some shell said this",
		"@@live\tclaude-api\t/home/me/api\t1700000000\tbox\t✳ Fix the login bug",
		"@@count\t5\t812",
		"@@m\t800",
		"<  the line above",
		"=    result: Zephyr-Lantern ok",
		">  the line below",
		"@@m\t410",
		"<",
		"=zephyr-lantern at the start",
		">",
		"@@live\tshell-ab12\t/home/me\t1700000100\tbox\tbox",
		"@@count\t0\t40",
		"@@live\twork\t/tmp/x y\t1700000200\tbox\t",
		"@@count\t1\t3",
		"@@m\t2",
		"<$ ls",
		"=ZEPHYR-LANTERN.txt",
		">$",
		"@@livedone",
		"@@prog\t0\t12",
		"@@conv\t-home-me-api\taaaaaaaa-0000-0000-0000-000000000001\t1700000000\t5000\tcli",
		"c\t/home/me/api",
		"s\t2026-03-02T10:00:00.000Z",
		`t	{"type":"ai-title","aiTitle":"Rename the module","sessionId":"x"}`,
		`@@hit	user	2026-03-01T09:00:00.000Z	00	please rename the 	Zephyr-Lantern	 module\nthanks`,
		"@@hit\tassistant\t\t11\twords before \tzephyr-lantern\t and after",
		"@@hit\ttoo\tfew",
		"@@.",
		"@@prog\t7\t12",
		"@@conv\t-home-me-jobs\taaaaaaaa-0000-0000-0000-000000000002\t1700000300\t900\tsdk-py",
		"c\t/home/me/jobs",
		`t	{"type":"ai-title","aiTitle":"Zephyr-Lantern nightly","sessionId":"x"}`,
		"@@hit\ttitle\t\t00\t\tZephyr-Lantern nightly\t",
		"@@.",
		"@@done\t12\t12\tcap",
	}
	var parts [][]SearchHit
	progress := 0
	p := searchParser{machine: "box", query: "zephyr-lantern"}
	p.emit = func(h []SearchHit) {
		if h == nil {
			progress++
			return
		}
		parts = append(parts, h)
	}
	for _, l := range lines {
		p.line(l)
	}
	p.flush()
	if !p.done || p.why != "cap" || p.scanned != 12 || p.total != 12 || p.noPerl || !p.liveDone || progress != 2 {
		t.Errorf("state: %+v progress %d", p, progress)
	}
	if len(p.junk) != 1 || p.junk[0] != "some shell said this" {
		t.Errorf("what wasn't the script's: %q", p.junk)
	}
	// Handed over as each session and each conversation completes.
	if len(parts) != 4 || len(parts[0]) != 2 || len(parts[1]) != 1 || len(parts[2]) != 2 || len(parts[3]) != 1 {
		t.Fatalf("parts: %+v", parts)
	}
	a := parts[0][0]
	if a.Kind != "live" || a.Machine != "box" || a.Session != "claude-api" || a.Dir != "/home/me/api" || a.Title != "Fix the login bug" || a.Matches != 5 ||
		a.Snippet != "result: Zephyr-Lantern ok" || a.Before != "the line above" || a.After != "the line below" || !a.At.Equal(time.Unix(1700000000, 0)) {
		t.Errorf("live hit: %+v", a)
	}
	if b := parts[0][1]; b.Snippet != "zephyr-lantern at the start" || b.Before != "" || b.After != "" {
		t.Errorf("second live hit: %+v", b)
	}
	if w := parts[1][0]; w.Session != "work" || w.Dir != "/tmp/x y" || w.Title != "" || w.Snippet != "ZEPHYR-LANTERN.txt" || w.Before != "$ ls" {
		t.Errorf("third session: %+v", w)
	}
	u := parts[2][0]
	if u.Kind != "past" || u.ID != "aaaaaaaa-0000-0000-0000-000000000001" || u.Dir != "/home/me/api" || u.Title != "Rename the module" || u.Role != "user" || u.Auto ||
		u.Snippet != "please rename the Zephyr-Lantern module thanks" || !u.At.Equal(time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)) || !u.Updated.Equal(time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("past hit: %+v", u)
	}
	if c := parts[2][1]; c.Role != "assistant" || c.Snippet != "…words before zephyr-lantern and after…" || !c.At.Equal(c.Updated) {
		t.Errorf("second past hit: %+v", c)
	}
	if s := parts[3][0]; !s.Auto || s.Role != "title" || s.Snippet != "Zephyr-Lantern nightly" {
		t.Errorf("a title that matched: %+v", s)
	}

	// A machine without perl, and one that was cut off.
	q := searchParser{machine: "box", query: "x"}
	for _, l := range []string{"@@livedone", "@@noperl"} {
		q.line(l)
	}
	if !q.noPerl || q.done {
		t.Errorf("no perl: %+v", q)
	}
}

func TestMergeHits(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 3, 1, h, 0, 0, 0, time.UTC) }
	hits := []SearchHit{
		{Kind: "past", Machine: "box", ID: "old", Updated: at(1), Snippet: "old one"},
		{Kind: "past", Machine: "@local", ID: "script", Updated: at(9), Snippet: "a run", Auto: true},
		{Kind: "live", Machine: "box", Session: "quiet", At: at(2), Snippet: "quiet"},
		{Kind: "past", Machine: "box", ID: "new", Updated: at(8), At: at(3), Snippet: "first said"},
		{Kind: "past", Machine: "box", ID: "new", Updated: at(8), At: at(7), Snippet: "said later"},
		{Kind: "live", Machine: "mini", Session: "busy", At: at(6), Snippet: "busy 1"},
		{Kind: "live", Machine: "mini", Session: "busy", At: at(6), Snippet: "busy 2"},
		{Kind: "past", Machine: "@local", ID: "mid", Updated: at(5), Snippet: "middle"},
		{Kind: "past", Machine: "@local", ID: "mid", Updated: at(5), Snippet: "middle"}, // came twice
		{Kind: "past", Machine: "mini", ID: "mid", Updated: at(5), Snippet: "middle"},   // same words, another machine
	}
	var got []string
	for _, h := range mergeHits(hits) {
		got = append(got, h.Machine+"/"+h.Session+h.ID+": "+h.Snippet)
	}
	want := []string{
		"mini/busy: busy 1", "mini/busy: busy 2", "box/quiet: quiet",
		"box/new: first said", "box/new: said later", "@local/mid: middle", "mini/mid: middle", "box/old: old one",
		"@local/script: a run",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("merged:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(mergeHits(nil)) != 0 {
		t.Error("nothing to merge")
	}
}

func TestNameHits(t *testing.T) {
	hits := []SearchHit{
		{Kind: "live", Machine: "box", Session: "claude-api", Title: "from the terminal"},
		{Kind: "live", Machine: "box", Session: "shell-ab12"},
		{Kind: "past", Machine: "box", ID: "one"},
		{Kind: "past", Machine: "mini", ID: "one"},
	}
	nameHits(hits, []Session{
		{Machine: "box", Name: "claude-api", Claude: true, State: "working", Title: "Fix the login bug", SID: "one"},
		{Machine: "box", Name: "shell-ab12"},
	})
	if h := hits[0]; h.Title != "Fix the login bug" || !h.Claude || h.ID != "one" {
		t.Errorf("a Claude session: %+v", h)
	}
	if h := hits[1]; h.Title != "" || h.Claude || h.ID != "" {
		t.Errorf("a shell: %+v", h)
	}
	if hits[2].Live != "claude-api" || hits[3].Live != "" {
		t.Errorf("open right now: %+v %+v", hits[2], hits[3])
	}
}

// The real script against transcripts made here, and against a tmux server of the test's own.
func TestSearchScript(t *testing.T) {
	root := scriptHome(t)
	ctx := context.Background()
	e := New()

	api := who{cwd: "/work/api", day: "2026-03-05"}
	age(t, transcript(t, root, "-work-api", "22222222-aaaa-bbbb-cccc-000000000001",
		api.user("09:00:00", "please rename the Zephyr-Lantern module\nand say \"hello world\" in C:\\temp"),
		api.thinking("09:00:01", "the user wants zephyr-lantern renamed"),
		api.toolUse("09:00:02", "grep -r zephyr-lantern ."),
		api.toolResult("09:00:03", "src/zephyr-lantern.go: package zephyr-lantern"),
		api.assistant("09:00:04", "Done: the module is now called zephyr-lantern everywhere.\nWhat is next?"),
		api.user("09:00:05", "one more thing about ZEPHYR-LANTERN: add a test"),
		aiTitle("Rename a module"),
	), 1*time.Hour)
	// Only tools, notes and Claude's own thinking mention it: not a hit.
	tools := who{cwd: "/work/tools", day: "2026-03-04"}
	age(t, transcript(t, root, "-work-tools", "22222222-aaaa-bbbb-cccc-000000000002",
		tools.user("09:00:00", "list the files"),
		tools.thinking("09:00:01", "maybe zephyr-lantern is here"),
		tools.toolUse("09:00:02", "ls zephyr-lantern"),
		tools.toolResult("09:00:03", "zephyr-lantern.txt"),
		tools.user("09:00:04", "a note about zephyr-lantern", `"isMeta":true`),
		tools.user("09:00:05", "summary mentioning zephyr-lantern", `"isCompactSummary":true`),
		tools.user("09:00:06", "<task-notification>zephyr-lantern finished</task-notification>"),
		tools.assistant("09:00:07", "There is one file.\next step: nothing"),
		aiTitle("List files"),
	), 2*time.Hour)
	// Only its name says it.
	plan := who{cwd: "/work/plan", day: "2026-03-03"}
	age(t, transcript(t, root, "-work-plan", "22222222-aaaa-bbbb-cccc-000000000003",
		plan.user("09:00:00", "let us plan the next steps"),
		plan.assistant("09:00:01", "Here is a plan."),
		aiTitle("Zephyr-Lantern rollout plan"),
	), 3*time.Hour)
	// A megabyte of answer with the words near its end, and a picture with the words under it.
	huge := who{cwd: "/work/huge", day: "2026-03-02"}
	age(t, transcript(t, root, "-work-huge", "22222222-aaaa-bbbb-cccc-000000000004",
		huge.userBlocks("09:00:00", "what does this screenshot show?"),
		huge.assistant("09:00:01", strings.Repeat("lorem ipsum dolor ", 60000)+"and finally the zephyr-lantern appears "+strings.Repeat("sit amet ", 200)),
		aiTitle("A long answer"),
	), 4*time.Hour)
	// Started by a script.
	jobs := who{cwd: "/work/jobs", day: "2026-03-01", entry: "sdk-ts"}
	age(t, transcript(t, root, "-work-jobs", "22222222-aaaa-bbbb-cccc-000000000005",
		jobs.user("09:00:00", "Check the zephyr-lantern service"),
		jobs.assistant("09:00:01", "It is up."),
	), 5*time.Hour)

	search := func(q string, o SearchOptions) SearchResult {
		t.Helper()
		o.Machines = []string{LocalMachine}
		if o.Sessions == nil {
			o.Sessions = []Session{}
		}
		res := e.Search(ctx, q, o)
		if len(res.Errors) != 0 {
			t.Fatalf("search %q: %v", q, res.Errors)
		}
		return res
	}
	past := func(res SearchResult) []string {
		var out []string
		for _, h := range res.Hits {
			if h.Kind == "past" {
				out = append(out, fmt.Sprintf("%s|%s|%s|auto=%v", h.ID[len(h.ID)-1:], h.Role, h.Snippet, h.Auto))
			}
		}
		return out
	}

	var streamed, ends int
	res := search("Zephyr-Lantern", SearchOptions{Each: func(p SearchPart) {
		streamed += len(p.Hits)
		if p.Done {
			ends++
			if p.Machine != LocalMachine || p.Error != "" || p.Partial || p.Total != 5 || p.Scanned != 5 {
				t.Errorf("the end: %+v", p)
			}
		}
	}})
	got := past(res)
	want := []string{
		"1|user|please rename the Zephyr-Lantern module and say \"hello world\" in C:\\temp|auto=false",
		"1|assistant|Done: the module is now called zephyr-lantern everywhere. What is next?|auto=false",
		"3|title|Zephyr-Lantern rollout plan|auto=false",
		"4|assistant|…dolor lorem ipsum dolor lorem ipsum dolor and finally the zephyr-lantern appears " + strings.Repeat("sit amet ", 15) + "sit…|auto=false",
		"5|user|Check the zephyr-lantern service|auto=true",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("hits:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if ends != 1 || streamed != len(res.Hits) || res.Scanned != 5 || res.Total != 5 {
		t.Errorf("streamed %d of %d hits, %d ends, %d/%d scanned", streamed, len(res.Hits), ends, res.Scanned, res.Total)
	}
	first := res.Hits[len(res.Hits)-len(got)]
	if first.Dir != "/work/api" || first.Title != "Rename a module" || first.Machine != LocalMachine ||
		!first.At.Equal(time.Date(2026, 3, 5, 9, 0, 0, 0, time.UTC)) || !first.Updated.Equal(time.Date(2026, 3, 5, 9, 0, 5, 0, time.UTC)) {
		t.Errorf("first hit: %+v", first)
	}

	// Without the runs of scripts; and only so many conversations.
	if got := past(search("zephyr-lantern", SearchOptions{Auto: -1})); len(got) != 4 || strings.Contains(strings.Join(got, "\n"), "auto=true") {
		t.Errorf("no scripts: %q", got)
	}
	if got := past(search("zephyr-lantern", SearchOptions{Limit: 1, Auto: -1})); len(got) != 2 || !strings.HasPrefix(got[0], "1|") || !strings.HasPrefix(got[1], "1|") {
		t.Errorf("one conversation: %q", got)
	}
	// Quotes and backslashes are found as typed (the file has them escaped).
	if got := past(search(`"hello world"`, SearchOptions{})); len(got) != 1 || !strings.Contains(got[0], `say "hello world" in`) {
		t.Errorf("quotes: %q", got)
	}
	if got := past(search(`C:\temp`, SearchOptions{})); len(got) != 1 || !strings.HasPrefix(got[0], "1|user|") {
		t.Errorf("backslash: %q", got)
	}
	// "next" is in "What is next?" and in "plan the next steps", but not in "file.\next step",
	// where the n belongs to the line break before "ext".
	if got := past(search("next", SearchOptions{})); len(got) != 2 || !strings.HasPrefix(got[0], "1|assistant|") || !strings.HasPrefix(got[1], "3|user|") {
		t.Errorf("an escape is not a letter: %q", got)
	}
	if got := past(search("ext step", SearchOptions{})); len(got) != 2 || !strings.HasPrefix(got[0], "2|assistant|") || !strings.Contains(got[0], "file. ext step: nothing") {
		t.Errorf("after a line break: %q", got)
	}
	if res := search("no-such-words-anywhere", SearchOptions{}); len(res.Hits) != 0 || res.Scanned != 5 {
		t.Errorf("nothing: %+v", res)
	}
	// Split between several processes (as on a machine with many conversations), the result
	// is the same, in the same order.
	for _, q := range []string{"zephyr-lantern", "next", "ext step", "no-such-words-anywhere"} {
		for _, o := range []SearchOptions{{}, {Auto: -1}, {Limit: 1, Auto: -1}, {Limit: 2}} {
			searchJobsMin = 200
			one := search(q, o)
			searchJobsMin = 1
			many := search(q, o)
			searchJobsMin = 200
			if a, b := strings.Join(past(one), "\n"), strings.Join(past(many), "\n"); a != b || one.Total != many.Total || (o.Limit == 0 && one.Scanned != many.Scanned) {
				t.Errorf("%q %+v: several processes found\n%s\n(%d/%d), one found\n%s\n(%d/%d)", q, o, b, many.Scanned, many.Total, a, one.Scanned, one.Total)
			}
		}
	}
	// Too short to search for.
	if res := e.Search(ctx, " z ", SearchOptions{}); len(res.Hits) != 0 || res.Scanned != 0 {
		t.Errorf("one letter: %+v", res)
	}

	// Live sessions, on a tmux server of the test's own.
	if LocalTmux() == "" {
		t.Log("tmux is not installed: live sessions not tested")
		return
	}
	const name = "skytest-search"
	// Only the two sessions made here are ended; the server goes with its last session, and
	// the socket it leaves behind is removed.
	t.Cleanup(func() {
		_, _ = localSh(ctx, "tmux kill-session -t '="+name+"' 2>/dev/null; tmux kill-session -t '=skytest-quiet' 2>/dev/null; tmux list-sessions >/dev/null 2>&1 || rm -f \"${TMUX_TMPDIR:-/tmp}/tmux-$(id -u)/"+localSocket()+"\"; true")
	})
	show := `printf '%s\n' 'first line' 'above the match' '  found ZEPHYR-Lantern-7 here  ' 'below the match' '' 'zephyr-lantern again' 'the end'; sleep 120`
	if _, err := localSh(ctx, "tmux new-session -d -x 120 -y 30 -s "+name+" "+sshQuote(show)+" && tmux new-session -d -x 120 -y 30 -s skytest-quiet 'echo nothing to see; sleep 120'"); err != nil {
		t.Fatal(err)
	}
	var live []SearchHit
	for i := 0; i < 40 && len(live) < 2; i++ { // until the pane has printed
		time.Sleep(100 * time.Millisecond)
		live = nil
		for _, h := range search("zephyr-lantern", SearchOptions{Auto: -1}).Hits {
			if h.Kind == "live" {
				live = append(live, h)
			}
		}
	}
	if len(live) != 2 {
		t.Fatalf("live hits: %+v", live)
	}
	// The latest match first, each with the lines around it.
	if h := live[0]; h.Session != name || h.Machine != LocalMachine || h.Matches != 2 || h.Snippet != "zephyr-lantern again" || h.Before != "below the match" || h.After != "the end" || h.At.IsZero() {
		t.Errorf("latest live hit: %+v", h)
	}
	if h := live[1]; h.Snippet != "found ZEPHYR-Lantern-7 here" || h.Before != "above the match" || h.After != "below the match" {
		t.Errorf("earlier live hit: %+v", h)
	}
	// Open sessions come before conversations.
	all := search("zephyr-lantern", SearchOptions{Auto: -1}).Hits
	if len(all) != 6 || all[0].Kind != "live" || all[1].Kind != "live" || all[2].Kind != "past" {
		t.Errorf("order: %d hits", len(all))
	}

	// Scrolling the session to the match: copy mode, on the latest line that has it.
	if err := e.ShowMatch(ctx, LocalMachine, name, "Zephyr-Lantern"); err != nil {
		t.Fatal(err)
	}
	out, err := localSh(ctx, "tmux display-message -p -t '="+name+":' '#{pane_in_mode}|#{copy_cursor_line}'")
	if err != nil || strings.TrimSpace(out) != "1|zephyr-lantern again" {
		t.Errorf("after ShowMatch: %q %v", out, err)
	}
	// Again from the bottom, not from where the last search left the cursor.
	if err := e.ShowMatch(ctx, LocalMachine, name, "zephyr-lantern"); err != nil {
		t.Fatal(err)
	}
	if out, _ := localSh(ctx, "tmux display-message -p -t '="+name+":' '#{pane_in_mode}|#{copy_cursor_line}'"); strings.TrimSpace(out) != "1|zephyr-lantern again" {
		t.Errorf("after a second ShowMatch: %q", out)
	}
	if out, _ := localSh(ctx, "tmux display-message -p -t '=skytest-quiet:' '#{pane_in_mode}'"); strings.TrimSpace(out) != "0" {
		t.Errorf("another session was touched: %q", out)
	}
}

func sshQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
