package artifact

import (
	"bytes"
	"encoding/json"
	"image/color"
	pngdec "image/png"
	"strings"
	"testing"
)

// The manifest names the app by its title, cut for a launcher, and
// scopes it to the page's folder; an untitled one takes its id.
func TestManifest(t *testing.T) {
	long := strings.Repeat("长", 30)
	for _, c := range []struct{ title, name, short string }{
		{"Board", "Board", "Board"},
		{"  ", "abc", "abc"},
		{long, long, strings.Repeat("长", 23) + "…"},
	} {
		var m map[string]any
		if err := json.Unmarshal(Manifest(&Artifact{ID: "abc", Title: c.title}, "/a/abc/", "/a/abc/_api"), &m); err != nil {
			t.Fatal(err)
		}
		if m["name"] != c.name || m["short_name"] != c.short || m["start_url"] != "/a/abc/" || m["scope"] != "/a/abc/" || m["id"] != "/a/abc/" || m["display"] != "standalone" {
			t.Fatalf("%q: %v", c.title, m)
		}
		icons, _ := m["icons"].([]any)
		if len(icons) != len(AppIconSizes) || icons[0].(map[string]any)["src"] != "/a/abc/_api/app-icon-192.png" {
			t.Fatalf("icons: %v", icons)
		}
	}
}

// An icon is its size, keeps its colour by its id, has the title's
// letter in white (or a page with no letter the font has), and its
// corners are clear.
func TestAppIconPNG(t *testing.T) {
	white := func(title string) int {
		b, err := AppIconPNG(&Artifact{ID: "abc", Title: title}, 192)
		if err != nil {
			t.Fatal(err)
		}
		img, err := pngdec.Decode(bytes.NewReader(b))
		if err != nil || img.Bounds().Dx() != 192 {
			t.Fatalf("%q: %v %v", title, err, img.Bounds())
		}
		if _, _, _, a := img.At(2, 2).RGBA(); a != 0 {
			t.Fatalf("%q: the corner is drawn", title)
		}
		n := 0
		for y := 0; y < 192; y++ {
			for x := 0; x < 192; x++ {
				if color.RGBAModel.Convert(img.At(x, y)) == (color.RGBA{255, 255, 255, 255}) {
					n++
				}
			}
		}
		return n
	}
	m, i, page := white("matrix"), white("  — 1 item"), white("看板")
	if m == 0 || i == 0 || page == 0 || m == i {
		t.Fatalf("white pixels: M %d, 1 %d, page %d", m, i, page)
	}
	if white("Matrix") != m {
		t.Fatal("the letter isn't upper-cased")
	}
	a, _ := AppIconPNG(&Artifact{ID: "abc", Title: "X"}, 512)
	b, _ := AppIconPNG(&Artifact{ID: "abc", Title: "X"}, 512)
	if !bytes.Equal(a, b) {
		t.Fatal("the same artifact draws another icon")
	}
}

// The worker answers only the app's window, falling back to a page in
// the language asked for, or picked from the browser's.
func TestAppWorkerJS(t *testing.T) {
	zh := string(AppWorkerJS("zh"))
	if !strings.Contains(zh, `r.mode !== "navigate" || r.destination !== "document"`) || !strings.Contains(zh, "e.preloadResponse") || strings.Contains(zh, "fetch(r).catch") || !strings.Contains(zh, "magpie 没有在运行") || strings.Contains(zh, "navigator.language") {
		t.Fatalf("zh: %s", zh)
	}
	sys := string(AppWorkerJS("system"))
	if !strings.Contains(sys, "magpie isn't running") || !strings.Contains(sys, "navigator.language") || !strings.Contains(sys, "Öffne magpie") {
		t.Fatalf("system: %s", sys)
	}
}
