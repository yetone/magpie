package gui

import "testing"

// A closed window's webview goes on the second look in a row that finds it
// closed with lightweight mode on (#580), never while it is shown or the
// mode is off, and a window shown between looks starts over.
func TestLightStep(t *testing.T) {
	type look struct{ on, shown, release bool }
	for name, looks := range map[string][]look{
		"closed":         {{true, false, false}, {true, false, true}},
		"off":            {{false, false, false}, {false, false, false}, {false, false, false}},
		"shown":          {{true, true, false}, {true, true, false}, {true, true, false}},
		"shown between":  {{true, false, false}, {true, true, false}, {true, false, false}, {true, false, true}},
		"turned off":     {{true, false, false}, {false, false, false}, {true, false, false}, {true, false, true}},
		"again after it": {{true, false, false}, {true, false, true}, {true, false, false}, {true, false, true}},
	} {
		ticks := 0
		for i, l := range looks {
			var release bool
			ticks, release = lightStep(l.on, l.shown, ticks)
			if release != l.release {
				t.Fatalf("%s: look %d released %v", name, i, release)
			}
			if release {
				ticks = 0 // as lighten does once the window is let go
			}
		}
	}
}

// The Mac's tray panel is made when it is first opened, not at start: a
// hidden one kept a WebContent process of ~67 MB for a user who never
// opened it. Elsewhere it is still made at start.
func TestPanelOnFirstOpen(t *testing.T) {
	for goos, want := range map[string]bool{"darwin": true, "linux": false, "windows": false} {
		if got := panelOnFirstOpen(goos); got != want {
			t.Errorf("panelOnFirstOpen(%s) = %v, want %v", goos, got, want)
		}
	}
}

func TestMainOnFirstOpen(t *testing.T) {
	for _, c := range []struct {
		goos     string
		showMain bool
		want     bool
	}{
		{"darwin", false, true},
		{"darwin", true, false},
		{"linux", false, false},
		{"windows", false, false},
	} {
		if got := mainOnFirstOpen(c.goos, c.showMain); got != c.want {
			t.Errorf("mainOnFirstOpen(%q, %v) = %v, want %v", c.goos, c.showMain, got, c.want)
		}
	}
}
