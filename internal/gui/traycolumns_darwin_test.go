package gui

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A card with four windows draws, through AppKit, two columns of two
// stacked rows (jen_celeus on X), in light and dark: as high as a card
// with two, up to about twice as wide, with a gap of no ink between its
// columns; with three, the third alone on the top line of its column.
func TestTrayImageColumns(t *testing.T) {
	bird, err := os.ReadFile("tray.png")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 15, 3, 0, 0, 0, time.UTC)
	q := antigravityPools()
	four, _, _ := trayCardsView("en", []provider.SubscriptionQuota{q}, now, false, false, nil)
	var kinds []string
	for _, w := range withTrayKinds([]provider.SubscriptionQuota{q})[0].Windows {
		kinds = append(kinds, w.TrayKind)
	}
	two, _, _ := trayCardsView("en", []provider.SubscriptionQuota{q}, now, false, false, map[string][]string{"antigravity": {kinds[1], kinds[3]}})
	three, _, _ := trayCardsView("en", []provider.SubscriptionQuota{q}, now, false, false, map[string][]string{"antigravity": {kinds[1], kinds[3], kinds[0]}})
	if len(four[0].Rows) != 4 || len(two[0].Rows) != 2 || len(three[0].Rows) != 3 {
		t.Fatalf("rows %q %q %q", four[0].Rows, two[0].Rows, three[0].Rows)
	}
	out := os.Getenv("MAGPIE_TRAY_PREVIEW")
	for _, dark := range []bool{false, true} {
		_, w0, _ := trayImagePNG(nil, bird, 22, 2, dark, false)
		_, w2, _ := trayImagePNG(two, bird, 22, 2, dark, false)
		b, w4, h := trayImagePNG(four, bird, 22, 2, dark, false)
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil || h != 44 {
			t.Fatalf("dark %v: %v, %dx%d", dark, err, w4, h)
		}
		// the digits after the logo (14pt and 3pt, at 2x)
		digits2, digits4 := w2-w0-34, w4-w0-34
		if digits4 < digits2+30 || digits4 > digits2*2+20 {
			t.Errorf("dark %v: four windows' digits are %d px wide, two's %d", dark, digits4, digits2)
		}
		left := image.Rect(w0+34, 0, w0+34+digits4/2-6, h)
		right := image.Rect(w0+34+digits4/2+6, 0, w4, h)
		if l, r := inkBands(img, left, dark), inkBands(img, right, dark); l != 2 || r != 2 {
			t.Errorf("dark %v: the columns draw %d and %d lines, want 2 and 2", dark, l, r)
		}
		// a gap of no ink between the columns
		gap := false
		for x := w0 + 34 + digits4/4; x < w4-digits4/4; x++ {
			if inkIn(img, image.Rect(x, 0, x+1, h), dark) == 0 {
				gap = true
				break
			}
		}
		if !gap {
			t.Errorf("dark %v: no gap between the columns", dark)
		}
		b3, w3, _ := trayImagePNG(three, bird, 22, 2, dark, false)
		img3, _ := png.Decode(bytes.NewReader(b3))
		top := inkIn(img3, image.Rect(w3-10, 0, w3, h/2), dark)
		bottom := inkIn(img3, image.Rect(w3-10, h/2+2, w3, h), dark)
		if top < 20 || bottom != 0 {
			t.Errorf("dark %v: the third window's column: %d px of ink on top, %d below", dark, top, bottom)
		}
		if out != "" {
			for name, cs := range map[string][]trayCell{"two": two, "three": three, "four": four, "plain": trayPlain(four)} {
				bg, _, _ := trayImagePNG(cs, bird, 22, 2, dark, true)
				f := filepath.Join(out, "tray-columns-"+name+map[bool]string{false: "-light.png", true: "-dark.png"}[dark])
				if err := os.WriteFile(f, bg, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}
