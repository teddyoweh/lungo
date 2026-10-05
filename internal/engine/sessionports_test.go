package engine

import (
	"reflect"
	"testing"

	"skybuild/internal/model"
)

func TestParseSessionPorts(t *testing.T) {
	linux := `@@panes@@
web	100
api	200
@@procs@@
    1     0 systemd
  100    50 zsh
  110   100 npm
  111   110 node
  200    50 -zsh
  210   200 /usr/bin/python3
  300     1 sshd
@@listen@@
SS
LISTEN 0 511 0.0.0.0:3000 0.0.0.0:* users:(("node",pid=111,fd=23))
LISTEN 0 511 [::]:3000 [::]:* users:(("node",pid=111,fd=24))
LISTEN 0 128 127.0.0.1:8000 0.0.0.0:* users:(("python3",pid=210,fd=3))
LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=300,fd=3))
LISTEN 0 4096 127.0.0.53%lo:53 0.0.0.0:*
`
	got := parseSessionPorts(linux)
	want := map[string][]model.Port{
		"web": {{Port: 3000, Address: "0.0.0.0", Process: "node"}},
		"api": {{Port: 8000, Address: "127.0.0.1", Process: "python3"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("linux:\n got %+v\nwant %+v", got, want)
	}

	mac := `@@panes@@
shell-ab12	500
@@procs@@
  500   400 -zsh
  510   500 node
  600     1 /usr/libexec/rapportd
@@listen@@
LSOF
p510
n*:5173
n[::1]:5173
p600
n*:49152
`
	got = parseSessionPorts(mac)
	want = map[string][]model.Port{"shell-ab12": {{Port: 5173, Address: "*", Process: "node"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("macOS:\n got %+v\nwant %+v", got, want)
	}
	if got := parseSessionPorts(""); len(got) != 0 {
		t.Errorf("nothing: %+v", got)
	}
}
