package gui

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// Several magpies added to the Dock, each named in its tab's title, all
// had the one black bird (eachg0 on X). With an accent of the user's own,
// magpie web's favicon, the home screen's icons and the manifest draw the
// bird in that colour, and two accents draw two birds; with none, each is
// the build's own file, as before. The icons and the manifest are still
// served without the key, as a home screen may ask for them.
func TestWebAppIconsTakeTheAccent(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("APPDATA", filepath.Join(h, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(h, "AppData", "Local"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(h, ".local", "share"))
	prior := onOmarchy
	onOmarchy = false
	t.Cleanup(func() { onOmarchy = prior })
	const key = "0123456789abcdef-key"
	srv := webHandler("3430", key, "", nil, webPageHandler(t, webHost{}, nil))
	get := func(path string, signedIn bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		if signedIn {
			r.AddCookie(&http.Cookie{Name: "magpie_web_3430", Value: key})
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s (signed in %v): %d", path, signedIn, rec.Code)
		}
		return rec
	}
	own := func(name string) []byte {
		b, err := fs.ReadFile(staticFS(), name)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	accent := func(a string) {
		s, err := settings.LoadChecked()
		if err != nil {
			t.Fatal(err)
		}
		s.Accent = a
		if err := settings.Save(s); err != nil {
			t.Fatal(err)
		}
	}
	decode := func(path string, b []byte) image.Image {
		t.Helper()
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		return img
	}
	near := func(c color.Color, want rgb) bool {
		r, g, b, _ := c.RGBA()
		d := func(v uint32, w float64) float64 { return float64(v>>8) - w }
		x, y, z := d(r, want[0]), d(g, want[1]), d(b, want[2])
		return x*x+y*y+z*z < 6*6
	}

	// magpie's own look: the build's files, byte for byte
	for _, f := range []struct {
		path     string
		signedIn bool
	}{{"/icons/favicon.svg", true}, {"/icons/apple-touch-icon.png", false}, {"/icons/icon-512.png", false}, {"/manifest.json", false}} {
		if got := get(f.path, f.signedIn).Body.Bytes(); !bytes.Equal(got, own(strings.TrimPrefix(f.path, "/"))) {
			t.Errorf("%s with no accent is not the build's file", f.path)
		}
	}
	if boot := get("/boot.js", true).Body.String(); strings.Contains(boot, "apple-touch-icon") {
		t.Errorf("boot.js changes the icons with no accent: %s", boot)
	}

	// the default icon's ink and ground: a black pixel of the bird and a
	// white one of its belly
	base := decode("icon-512.png", own("icons/icon-512.png"))
	var inkAt, whiteAt image.Point
	for y := 0; y < 512; y++ {
		for x := 0; x < 512; x++ {
			r, _, _, _ := base.At(x, y).RGBA()
			if r>>8 == 0 && inkAt == (image.Point{}) {
				inkAt = image.Pt(x, y)
			}
			if r>>8 == 255 && x > 200 && y > 300 && whiteAt == (image.Point{}) {
				whiteAt = image.Pt(x, y)
			}
		}
	}

	seen := map[string]string{}
	for _, a := range []string{"#0d9488", "#ea580c"} {
		accent(a)
		c, _ := parseHex(a)
		ink := markInk(c)
		q := "?accent=" + a[1:]

		boot := get("/boot.js", true).Body.String()
		if !strings.Contains(boot, `"icon":"icons/favicon.svg`+q+`"`) || !strings.Contains(boot, `"touch":"icons/apple-touch-icon.png`+q+`"`) {
			t.Errorf("%s: boot.js names no icons of the accent: %s", a, boot)
		}
		var css struct{ Mark appMark }
		json.Unmarshal(get("/api/appearance/css", true).Body.Bytes(), &css)
		if css.Mark.Icon != "icons/favicon.svg"+q || css.Mark.Light != ground(lightBg, c, false).hex() || css.Mark.Dark != ground(darkBg, c, true).hex() {
			t.Errorf("%s: /api/appearance/css's mark %+v", a, css.Mark)
		}

		svg := get("/icons/favicon.svg"+q, true)
		if ct := svg.Header().Get("Content-Type"); ct != "image/svg+xml" || !strings.Contains(svg.Body.String(), "path{fill:"+ink.hex()+"}") || !strings.Contains(svg.Body.String(), "path{fill:"+accentOn(c, true).hex()+"}") {
			t.Errorf("%s: favicon %q %s", a, ct, svg.Body.String()[:200])
		}
		seen["svg"+a] = svg.Body.String()

		var mf struct {
			Name       string `json:"name"`
			ThemeColor string `json:"theme_color"`
			Icons      []struct{ Src, Sizes string }
		}
		rec := get("/manifest.json", false)
		if err := json.Unmarshal(rec.Body.Bytes(), &mf); err != nil || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("%s: manifest %q %v", a, rec.Header().Get("Content-Type"), err)
		}
		if mf.Name != "magpie" || mf.ThemeColor != ground(lightBg, c, false).hex() || len(mf.Icons) != 2 {
			t.Errorf("%s: manifest %+v", a, mf)
		}
		for _, ic := range mf.Icons {
			if !strings.HasSuffix(ic.Src, q) {
				t.Errorf("%s: the manifest's %s doesn't name the accent", a, ic.Src)
			}
			// asked for as the manifest names it, without the cookie
			rec := get("/"+ic.Src, false)
			img := decode(ic.Src, rec.Body.Bytes())
			if got := img.Bounds().Dx(); got != map[string]int{"180x180": 180, "512x512": 512}[ic.Sizes] {
				t.Errorf("%s: %s is %dpx, named %s", a, ic.Src, got, ic.Sizes)
			}
			if rec.Header().Get("Content-Type") != "image/png" {
				t.Errorf("%s: %s is %q", a, ic.Src, rec.Header().Get("Content-Type"))
			}
			seen[ic.Sizes+a] = rec.Body.String()
			if ic.Sizes != "512x512" {
				continue
			}
			if got := img.At(inkAt.X, inkAt.Y); !near(got, ink) {
				t.Errorf("%s: the bird's ink at %v is %v, want %s", a, inkAt, got, ink.hex())
			}
			if got := img.At(whiteAt.X, whiteAt.Y); !near(got, white) {
				t.Errorf("%s: the belly at %v is %v, want white", a, whiteAt, got)
			}
		}
	}
	for _, k := range []string{"svg", "180x180", "512x512"} {
		if seen[k+"#0d9488"] == seen[k+"#ea580c"] {
			t.Errorf("two accents draw the same %s", k)
		}
	}

	// back to magpie's own look, the build's files again
	accent("")
	if got := get("/icons/icon-512.png", false).Body.Bytes(); !bytes.Equal(got, own("icons/icon-512.png")) {
		t.Error("the icon kept the accent once it was taken away")
	}
	var css struct{ Mark appMark }
	json.Unmarshal(get("/api/appearance/css", true).Body.Bytes(), &css)
	if css.Mark != defaultMark {
		t.Errorf("no accent: mark %+v", css.Mark)
	}
}

// defaultMark is index.html's own, so taking an accent away puts the page
// back as it was served.
func TestDefaultMarkIsIndexHTML(t *testing.T) {
	page := string(func() []byte { b, _ := fs.ReadFile(staticFS(), "index.html"); return b }())
	for _, want := range []string{
		`<link rel="icon" href="` + defaultMark.Icon + `"`,
		`<link rel="apple-touch-icon" href="` + defaultMark.Touch + `"`,
		`(prefers-color-scheme: light)" content="` + defaultMark.Light + `"`,
		`(prefers-color-scheme: dark)" content="` + defaultMark.Dark + `"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("index.html lacks %s", want)
		}
	}
	if _, ok := markSVG(func() []byte { b, _ := fs.ReadFile(staticFS(), "icons/favicon.svg"); return b }(), rgb{13, 148, 136}); !ok {
		t.Error("favicon.svg no longer has the fills markSVG draws in the accent")
	}
}
