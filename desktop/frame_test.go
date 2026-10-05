package main

import "testing"

func TestStartFrame(t *testing.T) {
	t.Setenv("SKYBUILD_HOME", t.TempDir())
	if _, ok := startFrame(mainWindow); ok {
		t.Fatal("nothing saved: the first window should fill the screen, not get a frame")
	}
	if _, ok := startFrame("w2"); ok {
		t.Fatal("nothing saved: a second window should fill the screen too")
	}
	first := frame{X: 100, Y: 200, W: 1500, H: 900, Fullscreen: true}
	updateFrames(func(m map[string]frame) { m[mainWindow] = first })
	if f, ok := startFrame(mainWindow); !ok || f != first {
		t.Fatalf("first window: got %+v %v, want its saved frame", f, ok)
	}
	f, ok := startFrame("w2")
	if !ok || f.W != 1500 || f.H != 900 || f.X != 130 || f.Y != 200+frameDown*30 || f.Fullscreen {
		t.Fatalf("second window: got %+v %v, want the first one's size, offset, not full screen", f, ok)
	}
	own := frame{X: 5, Y: 6, W: 800, H: 600}
	updateFrames(func(m map[string]frame) { m["w2"] = own })
	if f, ok := startFrame("w2"); !ok || f != own {
		t.Fatalf("second window with its own frame: got %+v %v", f, ok)
	}
	// A frame too small to be a window is ignored.
	updateFrames(func(m map[string]frame) { m[mainWindow] = frame{W: 10, H: 10} })
	if _, ok := startFrame(mainWindow); ok {
		t.Fatal("a broken saved frame was used")
	}
}
