//go:build darwin && cgo

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

// Without their logos the cells are their digits alone: narrower than with
// them, nothing drawn where the logo was, and a thin line between one card
// and the next. MAGPIE_TRAY_PREVIEW=<dir> writes them as PNGs to look at.
func TestTrayImagePlain(t *testing.T) {
	bird, err := os.ReadFile("tray.png")
	if err != nil {
		t.Fatal(err)
	}
	cells, _, _ := trayUsageView(trayCards(), time.Now(), false)
	cells = cells[:2]
	plain := trayPlain(cells)
	for _, dark := range []bool{false, true} {
		_, logoW, _ := trayImagePNG(cells, bird, 22, 2, dark, false)
		b, w, h := trayImagePNG(plain, bird, 22, 2, dark, false)
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		// at 2x: the bird 44 px, then 2pt, 3pt, the first card's digits
		_, oneW, _ := trayImagePNG(plain[:1], bird, 22, 2, dark, false)
		_, birdW, _ := trayImagePNG(nil, bird, 22, 2, dark, false)
		// two logos and their gaps (14+3 each) gone, 3 before the first
		// card's digits and the line's 5+1+5 for the 8 between cards come
		if d, want := logoW-w, 2*(2*(14+3)-3-(5+1+5-8)); d < want-2 || d > want+2 {
			t.Errorf("dark %v: %d px with logos, %d without", dark, logoW, w)
		}
		// right after the bird come the digits, not an empty logo's room
		if ink := inkIn(img, image.Rect(birdW, 0, birdW+10, h), dark); ink < 10 {
			t.Errorf("dark %v: %d px of ink where the first card's digits start", dark, ink)
		}
		// the line between the two: faint, a column of its own in the gap,
		// nothing on either side of it
		gap := image.Rect(oneW-1, 0, oneW-1+2*(5+1+5), h)
		line := 0
		for x := gap.Min.X; x < gap.Max.X; x++ {
			n := 0
			for y := 0; y < h; y++ {
				if _, _, _, a := img.At(x, y).RGBA(); a > 0x1000 {
					n++
				}
			}
			if n >= 20 {
				line++
			} else if n > 2 {
				t.Errorf("dark %v: column %d has %d px, neither line nor gap", dark, x, n)
			}
		}
		if line < 1 || line > 3 {
			t.Errorf("dark %v: the line between cards is %d px wide", dark, line)
		}
		if ink := inkIn(img, image.Rect(gap.Max.X, 0, w, h), dark); ink < 60 {
			t.Errorf("dark %v: the second card has %d px of ink", dark, ink)
		}
		if out := os.Getenv("MAGPIE_TRAY_PREVIEW"); out != "" {
			bg, _, _ := trayImagePNG(plain, bird, 22, 2, dark, true)
			if err := os.WriteFile(filepath.Join(out, map[bool]string{false: "plain-light.png", true: "plain-dark.png"}[dark]), bg, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}
