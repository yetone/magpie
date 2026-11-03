package gui

import (
	"os"
	"regexp"
	"testing"
	"time"
)

// A double-click on the tray icon opens the main window on Windows, the one
// tray that reports it (isaac_bai on Discord); the Mac and Linux keep one
// click for the panel, since a double-click there is two clicks.
func TestTrayDoubleClickOpensTheWindowOnWindowsOnly(t *testing.T) {
	for goos, want := range map[string]bool{"windows": true, "darwin": false, "linux": false} {
		if got := trayDoubleClicks(goos); got != want {
			t.Errorf("%s: double-click opens the window = %v, want %v", goos, got, want)
		}
	}
	src, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	// the handler is set where it applies, opens the window, and the click
	// handler asks first whether it is a double-click's release
	if !regexp.MustCompile(`(?s)if trayDoubleClicks\(runtime\.GOOS\) \{\s*h\.tray\.OnDoubleClick\(func\(\) \{\s*double\.doubled\(time\.Now\(\)\)\s*h\.ShowMain\(""\)`).Match(src) {
		t.Error("app.go doesn't open the window on a double-click where trayDoubleClicks says")
	}
	if !regexp.MustCompile(`(?s)h\.tray\.OnClick\(func\(\) \{\s*if double\.swallow\(time\.Now\(\)\) \{\s*return`).Match(src) {
		t.Error("the tray's click handler doesn't let a double-click's release go first")
	}
}

// Windows sends a double-click as up, double-click, up: the last up is the
// double-click's and opens nothing, once; a click after it, or one long
// after, is a click again.
func TestTrayDoubleClickReleaseSwallowedOnce(t *testing.T) {
	t0 := time.Date(2026, 11, 3, 20, 28, 0, 0, time.UTC)
	var d trayDouble
	if d.swallow(t0) {
		t.Fatal("a click with no double-click before it was swallowed")
	}
	d.doubled(t0)
	if !d.swallow(t0.Add(120 * time.Millisecond)) {
		t.Fatal("the double-click's own release opened the panel again")
	}
	if d.swallow(t0.Add(time.Second)) {
		t.Fatal("the click after a double-click's release was swallowed too")
	}
	// a release that never came: the user's next click, long after, opens
	d.doubled(t0)
	if d.swallow(t0.Add(trayDoubleRelease + time.Second)) {
		t.Fatal("a click long after a double-click was swallowed")
	}
	if d.swallow(t0.Add(trayDoubleRelease + 2*time.Second)) {
		t.Fatal("a late click left the double-click waiting")
	}
}
