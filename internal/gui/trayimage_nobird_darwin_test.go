//go:build darwin && cgo

package gui

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"testing"
	"time"
)

// Without the bird (settings.TrayNoBird, KevinXC on Discord: the icon takes
// room in the menu bar) the cells are drawn alone: the image is the bird
// and its gap narrower, and what is left is the cells as they were, from
// the image's left edge.
func TestTrayImageNoBird(t *testing.T) {
	bird, err := os.ReadFile("tray.png")
	if err != nil {
		t.Fatal(err)
	}
	cells, _, _ := trayUsageView(trayCards(), time.Now(), false)
	for _, cs := range [][]trayCell{cells[:2], trayPlain(cells[:2])} {
		for _, dark := range []bool{false, true} {
			wb, ww, h := trayImagePNG(cs, bird, 22, 2, dark, false)
			nb, nw, nh := trayImagePNG(cs, nil, 22, 2, dark, false)
			// the bird 22pt and its 2pt gap, at 2x
			if d := ww - nw; d < 47 || d > 49 || nh != h {
				t.Fatalf("dark %v: %dx%d with the bird, %dx%d without", dark, ww, h, nw, nh)
			}
			with, err := png.Decode(bytes.NewReader(wb))
			if err != nil {
				t.Fatal(err)
			}
			without, err := png.Decode(bytes.NewReader(nb))
			if err != nil {
				t.Fatal(err)
			}
			if ink := inkIn(without, image.Rect(0, 0, 12, h), dark); cs[0].Plain && ink < 10 || !cs[0].Plain && ink < 20 {
				t.Errorf("dark %v plain %v: %d px of ink at the left edge, where the first card starts", dark, cs[0].Plain, ink)
			}
			off, differ := ww-nw, 0
			for y := 0; y < h; y++ {
				for x := 0; x < nw; x++ {
					_, _, _, a := with.At(x+off, y).RGBA()
					_, _, _, b := without.At(x, y).RGBA()
					if d := int(a) - int(b); d > 0x2000 || d < -0x2000 {
						differ++
					}
				}
			}
			if differ > nw*h/100 {
				t.Errorf("dark %v plain %v: %d of %d px differ from the cells drawn beside the bird", dark, cs[0].Plain, differ, nw*h)
			}
		}
	}
}
