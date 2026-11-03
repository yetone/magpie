package gui

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestTrayImageOneWindow draws, through AppKit, Claude's card with its
// 5-hour window alone (settings.TrayWindows, StrikingRaw on X): one line
// at the bar's own size, not two small stacked numbers, in a cell not much
// wider than both took.
func TestTrayImageOneWindow(t *testing.T) {
	bird, err := os.ReadFile("tray.png")
	if err != nil {
		t.Fatal(err)
	}
	cards := trayCards()[:2]
	both, _, _ := trayCardsView("en", cards, time.Now(), false, false, nil)
	one, _, _ := trayCardsView("en", cards, time.Now(), false, false, map[string][]string{"claude": {"5h"}})
	if len(both[0].Rows) != 2 || len(one[0].Rows) != 1 || one[0].Rows[0] != "42%" || len(one[1].Rows) != 2 {
		t.Fatalf("both %q %q, one %q %q", both[0].Rows, both[1].Rows, one[0].Rows, one[1].Rows)
	}
	out := os.Getenv("MAGPIE_TRAY_PREVIEW")
	for _, dark := range []bool{false, true} {
		_, w0, _ := trayImagePNG(nil, bird, 22, 2, dark, false)
		lines := map[string]int{}
		for name, cs := range map[string][]trayCell{"both": both[:1], "one": one[:1]} {
			b, w, h := trayImagePNG(cs, bird, 22, 2, dark, false)
			img, err := png.Decode(bytes.NewReader(b))
			if err != nil || h != 44 || img.Bounds().Dx() != w {
				t.Fatalf("dark %v %s: %v, %dx%d", dark, name, err, w, h)
			}
			// the right half of Claude's cell, past its logo: its digits
			lines[name] = inkBands(img, image.Rect(w0+(w-w0)/2, 0, w, h), dark)
		}
		if lines["both"] != 2 || lines["one"] != 1 {
			t.Errorf("dark %v: both windows draw %d lines, one %d", dark, lines["both"], lines["one"])
		}
		_, wb, _ := trayImagePNG(both, bird, 22, 2, dark, false)
		_, wo, _ := trayImagePNG(one, bird, 22, 2, dark, false)
		if wo > wb+12 {
			t.Errorf("dark %v: one window is %d px wide, both %d", dark, wo, wb)
		}
		if out != "" {
			for name, cs := range map[string][]trayCell{"both": both, "one": one} {
				bg, _, _ := trayImagePNG(cs, bird, 22, 2, dark, true)
				f := filepath.Join(out, "tray-windows-"+name+map[bool]string{false: "-light.png", true: "-dark.png"}[dark])
				if err := os.WriteFile(f, bg, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

// inkBands counts the runs of pixel rows in r that hold ink: the lines of
// text drawn there.
func inkBands(img image.Image, r image.Rectangle, dark bool) int {
	n, in := 0, false
	for y := r.Min.Y; y < r.Max.Y; y++ {
		on := inkIn(img, image.Rect(r.Min.X, y, r.Max.X, y+1), dark) > 0
		if on && !in {
			n++
		}
		in = on
	}
	return n
}
