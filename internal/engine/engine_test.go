package engine

import (
	"testing"
	"time"
)

func TestParsePortsSS(t *testing.T) {
	out := `SS
LISTEN 0      4096   127.0.0.53%lo:53        0.0.0.0:*
LISTEN 0      128          0.0.0.0:22        0.0.0.0:*
LISTEN 0      511          0.0.0.0:3000      0.0.0.0:*    users:(("node",pid=812,fd=21))
LISTEN 0      511        127.0.0.1:5432      0.0.0.0:*    users:(("postgres",pid=90,fd=5))
LISTEN 0      511     100.71.2.3:44113      0.0.0.0:*    users:(("tailscaled",pid=7,fd=9))
LISTEN 0      511             [::]:3000         [::]:*    users:(("node",pid=812,fd=22))
LISTEN 0      511                *:8080            *:*    users:(("bun",pid=1,fd=3))`
	got := parsePorts(out)
	want := []struct {
		port int
		proc string
	}{{3000, "node"}, {5432, "postgres"}, {8080, "bun"}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i, w := range want {
		if got[i].Port != w.port || got[i].Process != w.proc {
			t.Errorf("%d: got %+v want %+v", i, got[i], w)
		}
	}
}

func TestParsePortsLsof(t *testing.T) {
	out := `LSOF
COMMAND   PID  USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
node    48213 teddy   23u  IPv6 0x9c2b3e3f1c1d   0t0  TCP *:5173 (LISTEN)
rapportd  650 teddy    9u  IPv4 0x9c2b3e3f1c1e   0t0  TCP *:49152 (LISTEN)`
	got := parsePorts(out)
	if len(got) != 1 || got[0].Port != 5173 || got[0].Process != "node" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseSessions(t *testing.T) {
	now := time.Now().Unix()
	out := "main\t1700000000\t1700000100\t1\t2\t/home/teddy\tzsh\n" +
		"claude-api\t1700000200\t1700000300\t0\t1\t/home/teddy/code/api\tclaude\n" +
		"@@sky-status@@\n" +
		`{"session":"claude-api","state":"waiting","at":` + itoa(now) + `,"message":"Claude needs your permission to use Bash"}` + "\n"
	got := parseSessions("box", out)
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	c := got[0] // newest first
	if c.Name != "claude-api" || !c.Claude || c.State != "waiting" || c.Message == "" || c.Machine != "box" {
		t.Errorf("claude session: %+v", c)
	}
	if got[1].Claude || got[1].Attached != 1 || got[1].Windows != 2 {
		t.Errorf("main session: %+v", got[1])
	}
}

func TestSessionName(t *testing.T) {
	for in, want := range map[string]string{"claude-my repo!": "claude-my-repo", "  ": "", "a/b.c": "a-b-c"} {
		if got := SessionName(in); got != want {
			t.Errorf("SessionName(%q) = %q, want %q", in, got, want)
		}
	}
}

func itoa(n int64) string {
	b := []byte{}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestCleanTitle(t *testing.T) {
	cases := map[string]string{
		"✳ Multiple agents scan website": "Multiple agents scan website",
		"⠂ Fix the failing tests":        "Fix the failing tests",
		"demo":                           "",
		"teddy@demo: ~/code":             "",
		"Claude Code":                    "",
		"✳ Claude Code":                  "",
		"demo.c.core-spawn.internal":     "",
		"~/code/api":                     "~/code/api",
		"":                               "",
	}
	for in, want := range cases {
		if got := cleanTitle(in, "demo"); got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseSessionsTitle(t *testing.T) {
	out := "claude-api-4\t1700000200\t1700000300\t0\t1\t/home/teddy/code/api\tclaude\tdemo\t✳ Port the parser to Rust\n@@sky-status@@\n"
	got := parseSessions("demo", out)
	if len(got) != 1 || got[0].Title != "Port the parser to Rust" {
		t.Fatalf("got %+v", got)
	}
}
