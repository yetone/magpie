package gui

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The panel opens the main window from its edge nearest the tray: the
// foot above a bottom taskbar, the header below a top one (isaac_bai).
func TestPanelOpensTheWindowFromTheEdgeNearestTheTray(t *testing.T) {
	screen := panelRect{0, 0, 1920, 1080}
	for _, c := range []struct {
		name   string
		goos   string
		panel  panelRect
		screen panelRect
		known  bool
		want   string
	}{
		{"Windows, taskbar at the bottom", "windows", panelRect{1540, 520, 360, 520}, screen, true, "foot"},
		{"Windows, taskbar at the top", "windows", panelRect{1540, 48, 360, 520}, screen, true, "head"},
		{"Windows, a tall panel nearer the bottom", "windows", panelRect{1540, 100, 360, 932}, screen, true, "foot"},
		{"Windows, second screen above the first, taskbar at its bottom", "windows", panelRect{1540, -560, 360, 520}, panelRect{0, -1080, 1920, 1080}, true, "foot"},
		{"Windows, place unknown", "windows", panelRect{}, panelRect{}, false, "foot"},
		{"Linux X11, bottom panel", "linux", panelRect{1500, 520, 360, 520}, screen, true, "foot"},
		{"Linux X11, top bar", "linux", panelRect{1500, 32, 360, 520}, screen, true, "head"},
		{"Linux Wayland, place unknown", "linux", panelRect{0, 0, 360, 520}, screen, false, "head"},
		{"Linux, screen not read", "linux", panelRect{1500, 520, 360, 520}, panelRect{}, true, "head"},
		{"Mac menu bar", "darwin", panelRect{1500, 30, 360, 520}, screen, true, "head"},
		{"Mac, place unknown", "darwin", panelRect{}, panelRect{}, false, "head"},
	} {
		if got := panelOpenAt(c.goos, c.panel, c.screen, c.known); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	if js := panelOpenAtJS("foot"); !strings.Contains(js, `magpiePanelOpenAt("foot")`) {
		t.Errorf("foot: %s", js)
	}
	if js := panelOpenAtJS(`x");alert(1);("`); !strings.Contains(js, `magpiePanelOpenAt("head")`) {
		t.Errorf("only head or foot reach the page: %s", js)
	}
}

// Each way the tray shows the panel tells it its side once it is placed,
// and the page has the function that hears it.
func TestPanelToldItsSideWhenShown(t *testing.T) {
	src, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	body := regexp.MustCompile(`(?s)func \(h \*host\) togglePanelNow\(\) \{.*?\n\}\n`).Find(src)
	if body == nil {
		t.Fatal("togglePanelNow not found")
	}
	if !regexp.MustCompile(`(?s)PositionWindow\(h\.panel, 6\)\s*showWebview\(h\.panel\)\.Focus\(\)\s*h\.tellPanelSide\(\)\s*return`).Match(body) {
		t.Error("the Mac's panel isn't told its side after it is placed")
	}
	if !regexp.MustCompile(`(?s)h\.tray\.ToggleWindow\(\)\s*if h\.panel\.IsVisible\(\) \{\s*h\.tellPanelSide\(\)`).Match(body) {
		t.Error("the panel shown by ToggleWindow isn't told its side")
	}
	page, err := os.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), "window.magpiePanelOpenAt = (side)") {
		t.Error("app.js has no magpiePanelOpenAt")
	}
}
