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

// AppKit draws the A\ mark where Claude's logo was, in the bar's ink on
// light and dark bars alike: the logo's square differs from Claude's, and
// the digits beside it are where they were. MAGPIE_TRAY_PNG names a folder
// to write the images to, to look at them.
func TestTrayImageAnthropicMark(t *testing.T) {
	cells, _, _ := trayUsageView(trayCards()[:2], time.Now(), false, false)
	marked := trayAnthropic(cells)
	for _, dark := range []bool{false, true} {
		cb, cw, ch := trayImagePNG(cells, nil, 22, 2, dark, true)
		ab, aw, ah := trayImagePNG(marked, nil, 22, 2, dark, true)
		if cw != aw || ch != ah {
			t.Fatalf("dark %v: %dx%d with Claude's logo, %dx%d with the mark", dark, cw, ch, aw, ah)
		}
		if dir := os.Getenv("MAGPIE_TRAY_PNG"); dir != "" {
			name := map[bool]string{false: "light", true: "dark"}[dark]
			os.WriteFile(dir+"/claude-"+name+".png", cb, 0o644)
			os.WriteFile(dir+"/anthropic-"+name+".png", ab, 0o644)
		}
		claude, err := png.Decode(bytes.NewReader(cb))
		if err != nil {
			t.Fatal(err)
		}
		mark, err := png.Decode(bytes.NewReader(ab))
		if err != nil {
			t.Fatal(err)
		}
		// the first logo: 16pt at 2x from the left edge
		logo := image.Rect(0, 0, 32, ah)
		if ink := inkIn(mark, logo, dark); ink < 150 {
			t.Errorf("dark %v: %d px of the mark's ink", dark, ink)
		}
		if differ := pxDiffer(claude, mark, logo); differ < 50 {
			t.Errorf("dark %v: the mark differs from Claude's logo in %d px", dark, differ)
		}
		if differ := pxDiffer(claude, mark, image.Rect(34, 0, aw, ah)); differ > 0 {
			t.Errorf("dark %v: %d px differ past the first logo", dark, differ)
		}
	}
}

func pxDiffer(a, b image.Image, r image.Rectangle) int {
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			ar, ag, ab, aa := a.At(x, y).RGBA()
			br, bg, bb, ba := b.At(x, y).RGBA()
			if ar != br || ag != bg || ab != bb || aa != ba {
				n++
			}
		}
	}
	return n
}
