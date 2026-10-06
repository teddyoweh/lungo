package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewerVersion(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"0.1.4", "0.1.3", true},
		{"0.1.10", "0.1.9", true},
		{"v0.2.0", "0.1.99", true},
		{"0.1.3", "0.1.3", false},
		{"0.1.3", "0.1.4", false},
		{"1.0", "0.9.9", true},
		{"0.1.3", "dev", false}, // a development build is never offered an update
		{"junk", "0.1.3", false},
	} {
		if got := newer(c.a, c.b); got != c.want {
			t.Errorf("newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// A download whose checksum doesn't match the release's is refused before anything else.
func TestDownloadRefusesBadChecksum(t *testing.T) {
	t.Setenv("SKYBUILD_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("not the app")) }))
	defer srv.Close()
	m := releaseManifest{Version: "9.9.9", Mac: &releaseAsset{URL: srv.URL, SHA256: strings.Repeat("0", 64), Size: 11}}
	err := downloadUpdate(context.Background(), m, func(float64) {})
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err = %v, want a checksum error", err)
	}
	if _, err := os.Stat(readyApp()); err == nil {
		t.Fatal("a bad download was left ready to install")
	}
}

// The new app and the old trade places in one step.
func TestSwapApp(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	dir := t.TempDir()
	from, to := filepath.Join(dir, "ready", "Lungo.app"), filepath.Join(dir, "Applications", "Lungo.app")
	for path, v := range map[string]string{from: "new", to: "old"} {
		os.MkdirAll(path, 0o755)
		os.WriteFile(filepath.Join(path, "v"), []byte(v), 0o644)
	}
	if err := swapApp(from, to); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(to, "v")); string(b) != "new" {
		t.Fatalf("installed app is %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(from, "v")); string(b) != "old" {
		t.Fatalf("old app went to %q", b)
	}
}
