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

// An open panel the page resizes on Windows stays where the tray put it:
// its centre and the edge by the taskbar held, inside the work area
// (jiangshangmuyun on X: it opened flush right, then moved left).
func TestPanelRefitKeepsItsPlace(t *testing.T) {
	work := panelRect{0, 0, 1920, 1032} // taskbar at the bottom, 48 high
	for _, c := range []struct {
		name string
		at   panelRect
		w, h int
		want panelRect
	}{
		{"flush right, taller", panelRect{1554, 506, 360, 520}, 360, 700, panelRect{1554, 326, 360, 700}},
		{"flush right, shorter", panelRect{1554, 506, 360, 520}, 360, 400, panelRect{1554, 626, 360, 400}},
		{"by an icon, the same size", panelRect{1300, 506, 360, 520}, 360, 520, panelRect{1300, 506, 360, 520}},
		{"wider, held in the work area", panelRect{1554, 506, 360, 520}, 420, 520, panelRect{1500, 506, 420, 520}},
		{"taller than the work area", panelRect{1554, 506, 360, 520}, 360, 1100, panelRect{1554, -68, 360, 1100}},
		{"taskbar at the top", panelRect{1554, 54, 360, 520}, 360, 700, panelRect{1554, 54, 360, 700}},
		{"second screen to the left", panelRect{-366, 506, 360, 520}, 360, 600, panelRect{-366, 426, 360, 600}},
		{"work area not read", panelRect{1554, 506, 360, 520}, 360, 600, panelRect{1554, 426, 360, 600}},
	} {
		wa := work
		switch c.name {
		case "taskbar at the top":
			wa = panelRect{0, 48, 1920, 1032}
		case "second screen to the left":
			wa = panelRect{-1920, 0, 1920, 1032}
		case "work area not read":
			wa = panelRect{}
		}
		if got := panelRefit(c.at, wa, c.w, c.h); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

// Every refit of the open panel (the page's fit, its glide, a text size)
// goes through sizeOpenPanel, and on Windows that never asks the tray
// for the icon's place again: the icon can be in the overflow flyout as
// it opens and its arrow moments later.
func TestOpenPanelRefitsInPlaceOnWindows(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	app := read("app.go")
	for _, fn := range []string{"fitPanel", "zoomWindows"} {
		body := regexp.MustCompile(`(?s)func \(h \*host\) ` + fn + `\(.*?\n\}\n`).FindString(app)
		if body == "" {
			t.Fatalf("%s not found", fn)
		}
		if strings.Contains(body, "PositionWindow") || !strings.Contains(body, "h.sizeOpenPanel(h.panel") {
			t.Errorf("%s places the open panel by the icon again", fn)
		}
	}
	if g := read("glide_other.go"); strings.Contains(g, "PositionWindow") || !strings.Contains(g, "h.sizeOpenPanel(w, ") {
		t.Error("glidePanel places the panel by the icon at each step")
	}
	src := read("panelrefit.go")
	at := strings.Index(src, "\tif runtime.GOOS != \"windows\" {\n")
	if at < 0 {
		t.Fatal("sizeOpenPanel doesn't keep Windows apart")
	}
	win := src[at:]
	win = win[strings.Index(win, "return\n\t}")+len("return\n\t}"):]
	if strings.Contains(win, "PositionWindow") || !strings.Contains(win, "panelRefit(") || !strings.Contains(win, "SetBounds(") {
		t.Error("sizeOpenPanel's Windows path doesn't hold the panel's place")
	}
}
