package folders

import "testing"

func TestRemoteRel(t *testing.T) {
	for in, want := range map[string]string{"~": ".", "~/code/x": "code/x", "/etc/x": "/etc/x", "code": "code", "": "."} {
		if got := RemoteRel(in); got != want {
			t.Errorf("RemoteRel(%q) = %q, want %q", in, got, want)
		}
	}
}
