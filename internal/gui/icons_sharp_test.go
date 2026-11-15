package gui

import (
	"image"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The largest an icon is drawn in the GUI: the Routing page's nodes, 26px
// (plugins/routing/routing.css). Retina draws it at twice that.
const largestIconPx = 26

// Icons stay sharp at every size the GUI draws them (lexrus on X: the
// Antigravity icons were blurry in Library → Skills).
//
// An SVG drawn through <img> with an SVG filter in it (Antigravity's
// blurred colour blobs, WorkBuddy's glow) is rasterised by WebKit at one
// device pixel per CSS pixel and scaled up, so on a Retina screen the
// whole icon, edges included, is soft. Chromium draws it sharp, which is
// why it passes a Chromium-only look. A soft blob is a radial gradient
// instead. A PNG icon is at least twice its largest display size.
func TestIconsStaySharp(t *testing.T) {
	for _, dir := range []string{"assets/icons", "../../site/public/icons"} {
		svgs, _ := filepath.Glob(filepath.Join(dir, "*.svg"))
		if len(svgs) == 0 {
			t.Fatalf("no SVG icons in %s", dir)
		}
		for _, f := range svgs {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if s := string(b); strings.Contains(s, "<filter") || strings.Contains(s, "feGaussianBlur") {
				t.Errorf("%s has an SVG filter, which WebKit draws at 1x: blurry on Retina", f)
			}
		}
	}
	pngs, _ := filepath.Glob("assets/icons/*.png")
	if len(pngs) == 0 {
		t.Fatal("no PNG icons in assets/icons")
	}
	for _, f := range pngs {
		r, err := os.Open(f)
		if err != nil {
			t.Fatal(err)
		}
		c, _, err := image.DecodeConfig(r)
		r.Close()
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if min(c.Width, c.Height) < 2*largestIconPx {
			t.Errorf("%s is %dx%d, under twice the %dpx it can be drawn at", f, c.Width, c.Height, largestIconPx)
		}
	}
}
