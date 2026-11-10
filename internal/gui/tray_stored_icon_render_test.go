//go:build darwin && cgo

package gui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A picture a plugin gave, stored as a "file:…" one, is drawn in the menu
// bar's text colour as any other logo is (TestTrayImageMonoLogos): no
// colour in it, its shape what is opaque less what is near white. A tile
// of colour with a white glyph cut out (as plugins' logos often are) is
// drawn in ink with the glyph cut out, light and dark at 2x.
func TestTrayImageStoredIcon(t *testing.T) {
	bird, err := os.ReadFile("tray.png")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	// a 32x32 green tile with a white bird-shaped glyph in it
	src := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			switch {
			case x >= 8 && x < 24 && y >= 8 && y < 24 && (x+y)%4 != 0:
				src.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: 255}) // the glyph, near white
			default:
				src.SetNRGBA(x, y, color.NRGBA{R: 110, G: 195, B: 14, A: 255}) // the tile, green
			}
		}
	}
	var tile bytes.Buffer
	if err := png.Encode(&tile, src); err != nil {
		t.Fatal(err)
	}
	kept, err := provider.StoreIcon(tile.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	icon, mono := trayIconFile(kept)
	if mono || len(icon) == 0 {
		t.Fatalf("trayIconFile(%q) = %d bytes, mono %v", kept, len(icon), mono)
	}
	out := os.Getenv("MAGPIE_TRAY_PREVIEW")
	for _, dark := range []bool{false, true} {
		b, _, _ := trayImagePNG([]trayCell{{Icon: icon, Mono: mono, Rows: []string{"5%", "7%"}}}, bird, 22, 2, dark, true)
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		logo := image.Rect(48, 8, 48+28, 36)
		for y := logo.Min.Y; y < logo.Max.Y; y++ {
			for x := logo.Min.X; x < logo.Max.X; x++ {
				r, g, bl, _ := img.At(x, y).RGBA()
				if max(r, g, bl)-min(r, g, bl) > 0x1800 {
					t.Fatalf("stored icon, dark %v: colour at %d,%d: %x %x %x", dark, x, y, r>>8, g>>8, bl>>8)
				}
			}
		}
		ink := inkIn(img, logo, dark)
		if ink < 40 {
			t.Errorf("stored icon, dark %v: the logo has %d px of ink", dark, ink)
		}
		// the white glyph is cut out of the tile: the ink is less than the
		// whole box, as Codex's is (TestTrayImageMonoLogos)
		if ink > 28*28*85/100 {
			t.Errorf("stored icon, dark %v: %d px of ink, its glyph not cut out", dark, ink)
		}
		if out != "" {
			name := filepath.Join(out, "stored-"+map[bool]string{false: "light", true: "dark"}[dark]+".png")
			if err := os.WriteFile(name, b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}
