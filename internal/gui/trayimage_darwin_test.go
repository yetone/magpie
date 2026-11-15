//go:build darwin && cgo

package gui

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The menu bar's image: the bird, then a cell for each card side by side,
// drawn at twice the points for a Retina screen, in the bar's text colour
// light or dark. MAGPIE_TRAY_PREVIEW=<dir> writes each as a PNG to look at.
func TestTrayImage(t *testing.T) {
	bird, err := os.ReadFile("tray.png")
	if err != nil {
		t.Fatal(err)
	}
	cells, _, _ := trayUsageView(trayCards(), time.Now(), false, false)
	out := os.Getenv("MAGPIE_TRAY_PREVIEW")
	widths := map[bool][]int{}
	for _, dark := range []bool{false, true} {
		for n := 0; n <= 3; n++ {
			b, w, h := trayImagePNG(cells[:n], bird, 22, 2, dark, false)
			img, err := png.Decode(bytes.NewReader(b))
			if err != nil {
				t.Fatalf("%d cells: %v", n, err)
			}
			if h != 44 || img.Bounds().Dx() != w || img.Bounds().Dy() != h {
				t.Fatalf("%d cells: %dx%d px, image %v", n, w, h, img.Bounds())
			}
			widths[dark] = append(widths[dark], w)
			// the bird and each cell's digits are there, in the bar's text
			// colour: dark on the light bar, light on the dark one
			ink := inkIn(img, image.Rect(0, 0, 44, 44), dark)
			if ink < 40 {
				t.Errorf("%d cells, dark %v: the bird has %d px of ink", n, dark, ink)
			}
			// and the last card's, in what it adds to the width
			if n > 0 {
				if ink := inkIn(img, image.Rect(widths[dark][n-1], 0, w, h), dark); ink < 60 {
					t.Errorf("%d cells, dark %v: the last cell has %d px of ink", n, dark, ink)
				}
			}
			if out != "" {
				bg, _, _ := trayImagePNG(cells[:n], bird, 22, 2, dark, true)
				name := fmt.Sprintf("tray-%d-%s.png", n, map[bool]string{false: "light", true: "dark"}[dark])
				if err := os.WriteFile(filepath.Join(out, name), bg, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	// each card widens it, by no more than a modest cell
	for dark, ws := range widths {
		if ws[0] > 52 {
			t.Errorf("dark %v: the bird alone is %d px", dark, ws[0])
		}
		for i := 1; i < len(ws); i++ {
			if d := ws[i] - ws[i-1]; d < 40 || d > 110 {
				t.Errorf("dark %v: cell %d widens it by %d px", dark, i, d)
			}
		}
	}
	if widths[false][3] != widths[true][3] {
		t.Errorf("light %d px, dark %d px", widths[false][3], widths[true][3])
	}

	// a logo that can't be read, or none, is the name's letter in a ring
	b, _, _ := trayImagePNG([]trayCell{{Icon: []byte("not a picture"), Letter: "Z", Rows: []string{"5%", "7%"}}, {Letter: "Q", Rows: []string{"$3"}}}, bird, 22, 2, false, false)
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if ink := inkIn(img, image.Rect(48, 8, 48+28, 36), false); ink < 40 {
		t.Errorf("no letter where the logo can't be read: %d px", ink)
	}
}

// Hit-testing follows the image layout for both logo and plain cells.
func TestTrayImageCellAt(t *testing.T) {
	bird, err := os.ReadFile("tray.png")
	if err != nil {
		t.Fatal(err)
	}
	cells, _, _ := trayUsageView(trayCards(), time.Now(), false, false)
	const h = 22.0
	_, w, _ := trayImagePNG(cells, bird, h, 2, false, false)
	width := float64(w) / 2
	if at := trayImageCellAt(cells, h, 5); at != -1 {
		t.Errorf("a click by the bird says cell %d", at)
	}
	if at := trayImageCellAt(cells, h, width-0.5); at != len(cells)-1 {
		t.Errorf("a click at the image's end says cell %d, want %d", at, len(cells)-1)
	}
	if at := trayImageCellAt(cells, h, width+1); at != -1 {
		t.Errorf("a click past the image says cell %d", at)
	}
	if at := trayImageCellAt(nil, h, 5); at != -1 {
		t.Errorf("no cells: cell %d", at)
	}
	from := make([]float64, len(cells))
	for i := range cells {
		from[i] = trayImageCellStart(cells, h, i)
		if from[i] <= h || from[i] >= width {
			t.Fatalf("cell %d begins at %g of %g", i, from[i], width)
		}
		if i > 0 && from[i] <= from[i-1] {
			t.Fatalf("cell %d begins at %g, before the one before it at %g", i, from[i], from[i-1])
		}
	}
	for i := range cells {
		if at := trayImageCellAt(cells, h, from[i]+0.5); at != i {
			t.Errorf("a click at %g (cell %d's start) says cell %d", from[i], i, at)
		}
		if i+1 < len(cells) {
			if at := trayImageCellAt(cells, h, from[i+1]-0.5); at != i {
				t.Errorf("a click at %g (before cell %d) says cell %d", from[i+1]-0.5, i+1, at)
			}
		}
	}
	if s := trayImageCellStart(cells, h, len(cells)); s != -1 {
		t.Errorf("a cell past them all begins at %g", s)
	}
	// logos off (Settings): the same walk, the digits alone
	plain := trayPlain(cells)
	for i := range plain {
		if s := trayImageCellStart(plain, h, i); s <= h || s >= width {
			t.Errorf("plain cell %d begins at %g of %g", i, s, width)
		}
	}
	if at := trayImageCellAt(plain, h, trayImageCellStart(plain, h, 0)+0.5); at != 0 {
		t.Errorf("the first plain cell's click says cell %d", at)
	}
}

// inkIn counts the pixels in r drawn in the bar's text colour.
func inkIn(img image.Image, r image.Rectangle, dark bool) int {
	n := 0
	for y := r.Min.Y; y < r.Max.Y && y < img.Bounds().Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X && x < img.Bounds().Max.X; x++ {
			c, g, b, a := img.At(x, y).RGBA()
			if a < 0x8000 {
				continue
			}
			lum := (c + g + b) / 3 * 0xffff / max(a, 1)
			if dark && lum > 0xc000 || !dark && lum < 0x4000 {
				n++
			}
		}
	}
	return n
}

// A coloured logo is drawn as a template image is, in the bar's text colour
// alone (the user: 去掉色彩满足 tray icon 样式): no colour left in it, and
// Codex's white glyph on its blue tile is cut out of a tile of ink.
func TestTrayImageMonoLogos(t *testing.T) {
	bird, err := os.ReadFile("tray.png")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"codex-color", "claude-color", "gemini-color", "zcode", "alma", "antigravity-color", "workbuddy-color"} {
		icon, _ := trayIconFile(name)
		for _, dark := range []bool{false, true} {
			b, _, _ := trayImagePNG([]trayCell{{Icon: icon, Rows: []string{"5%", "7%"}}}, bird, 22, 2, dark, true)
			img, err := png.Decode(bytes.NewReader(b))
			if err != nil {
				t.Fatal(err)
			}
			logo := image.Rect(48, 8, 48+28, 36)
			for y := logo.Min.Y; y < logo.Max.Y; y++ {
				for x := logo.Min.X; x < logo.Max.X; x++ {
					r, g, b, _ := img.At(x, y).RGBA()
					if max(r, g, b)-min(r, g, b) > 0x1800 {
						t.Fatalf("%s, dark %v: colour at %d,%d: %x %x %x", name, dark, x, y, r>>8, g>>8, b>>8)
					}
				}
			}
			ink := inkIn(img, logo, dark)
			if ink < 40 {
				t.Errorf("%s, dark %v: the logo has %d px of ink", name, dark, ink)
			}
			if name == "codex-color" && ink > 28*28*85/100 {
				t.Errorf("codex, dark %v: %d px of ink, its glyph not cut out", dark, ink)
			}
		}
	}
}

// Each logo is as large as the next in the menu bar (the user: tray 上的
// provider icon 大小不一致), whatever margin its file has round it: its ink
// spans 12-14pt of the 14pt box, a solid tile the lower end.
func TestTrayImageLogoSizes(t *testing.T) {
	bird, err := os.ReadFile("tray.png")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"codex-color", "claude-color", "gemini-color", "zcode", "alma", "kimi", "openai", "deepseek-color", "qwen-color", "minimax-color", "antigravity-color", "workbuddy-color"} {
		icon, mono := trayIconFile(name)
		b, _, _ := trayImagePNG([]trayCell{{Icon: icon, Mono: mono, Rows: []string{"5%", "7%"}}}, bird, 22, 2, false, false)
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		x0, y0, x1, y1 := 99, 99, -1, -1
		for y := 0; y < 44; y++ {
			for x := 46; x < 78; x++ {
				if _, _, _, a := img.At(x, y).RGBA(); a > 0x4000 {
					x0, y0, x1, y1 = min(x0, x), min(y0, y), max(x1, x), max(y1, y)
				}
			}
		}
		if side := max(x1-x0, y1-y0) + 1; side < 24 || side > 29 {
			t.Errorf("%s spans %d px of a 28 px box", name, side)
		}
	}
}

// The native callback must carry the account ID before the drawing is freed
// or a refresh changes the order, including the logo-free layout.
func TestTrayImageClickIdentity(t *testing.T) {
	prior := onTrayCellClick
	t.Cleanup(func() { onTrayCellClick = prior })
	clicked := make(chan string, 1)
	onTrayCellClick = func(id string) { clicked <- id }
	cards := trayCards()
	cards[1].User = "a\"b\\c@例子.test"
	cells, _, _ := trayUsageView(cards, time.Now(), false, false)
	for _, layout := range [][]trayCell{cells, trayPlain(cells)} {
		if trayImageClickAt(layout, true, 22, 5, 0) {
			t.Fatal("bird intercepted")
		}
		x := trayImageCellStart(layout, 22, 1) + 1
		// AppKit's Shift, Control, Option and Command modifier bits.
		for _, modifiers := range []uint64{1 << 17, 1 << 18, 1 << 19, 1 << 20} {
			if trayImageClickAt(layout, true, 22, x, modifiers) {
				t.Fatal("modified click intercepted")
			}
		}
		if !trayImageClickAt(layout, true, 22, x, 0) {
			t.Fatal("cell missed")
		}
		select {
		case id := <-clicked:
			if id != trayCardID(cards[1]) {
				t.Fatalf("clicked %q", id)
			}
		case <-time.After(time.Second):
			t.Fatal("native click callback lost")
		}
	}
}

// Without the bird the cards are the whole item: a click anywhere on them is
// the item's own, which opens the panel on the tab left last, not
// Allowances (the owner: with the bird off, every click opened Allowances).
func TestTrayImageClickWithoutBird(t *testing.T) {
	prior := onTrayCellClick
	t.Cleanup(func() { onTrayCellClick = prior })
	clicked := make(chan string, 1)
	onTrayCellClick = func(id string) { clicked <- id }
	cells, _, _ := trayUsageView(trayCards(), time.Now(), false, false)
	for _, layout := range [][]trayCell{cells, trayPlain(cells)} {
		for i := range layout {
			x := trayImageCellStart(layout, 22, i) - (22 + 2) + 1 // the same cell, with no bird before it
			if trayImageClickAt(layout, false, 22, x, 0) {
				t.Fatalf("cell %d taken for its card with no bird", i)
			}
		}
	}
	select {
	case id := <-clicked:
		t.Fatalf("card %q opened", id)
	default:
	}
}

// The digits weigh what the menu bar's other figures do (qingnianxiaozhe on
// X: beside Surge's and iStat Menus' figures magpie's read too bold, and a
// lone figure too small and too heavy): two stacked rows in the small
// system font, regular or at most medium, never semibold or bold; a lone
// row in the menu bar's own font, its size and regular weight. Both have
// digits of one width.
func TestTrayRowFont(t *testing.T) {
	size, weight, mono, bar := trayRowFont(2)
	if size < 9.5 || size > 10.5 || weight > 0.25 || weight < 0 || !mono {
		t.Errorf("two rows: %.1fpt, weight %.2f, one-width digits %v; want 9.5-10.5pt, regular or medium, one width", size, weight, mono)
	}
	size, weight, mono, _ = trayRowFont(1)
	if size != bar || weight != 0 || !mono {
		t.Errorf("one row: %.1fpt, weight %.2f, one-width digits %v; want the menu bar's %.1fpt, regular, one width", size, weight, mono, bar)
	}
}
