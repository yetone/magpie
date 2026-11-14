package gui

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

func appearanceHome(t *testing.T) http.Handler {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("APPDATA", filepath.Join(h, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(h, "AppData", "Local"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(h, ".local", "share"))
	return Handler(&zoomWindows{}, nil)
}

func lookServe(srv http.Handler, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{200, 30, 30, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// An accent of the reader's own (CHENGJIAHEGOXIS on X) is saved from
// Settings, drawn by boot.js before the first paint, kept by a save that
// doesn't send it, and refused when it isn't a colour.
func TestAccentSetting(t *testing.T) {
	srv := appearanceHome(t)
	if rec := lookServe(srv, "POST", "/api/settings", `{"theme":"dark","lang":"en","accent":"#0d9488"}`); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got := settings.Load().Accent; got != "#0d9488" {
		t.Fatalf("saved accent %q", got)
	}
	boot := lookServe(srv, "GET", "/boot.js", "").Body.String()
	if !strings.Contains(boot, `"look":"#0d9488,"`) || !strings.Contains(boot, `e.id="lookCSS"`) || !strings.Contains(boot, "--accent:") {
		t.Fatalf("boot.js draws no accent: %s", boot)
	}
	// a save from somewhere that doesn't know the accent keeps it
	if rec := lookServe(srv, "POST", "/api/settings", `{"theme":"light","lang":"en"}`); rec.Code != http.StatusOK || settings.Load().Accent != "#0d9488" {
		t.Fatalf("a save without the accent changed it: %d %q", rec.Code, settings.Load().Accent)
	}
	for _, bad := range []string{"teal", "#0d948", "#0d9488;}body{display:none", "red;--x:1"} {
		b, _ := json.Marshal(map[string]string{"theme": "light", "lang": "en", "accent": bad})
		if rec := lookServe(srv, "POST", "/api/settings", string(b)); rec.Code == http.StatusOK || settings.Load().Accent != "#0d9488" {
			t.Fatalf("accent %q was taken: %d", bad, rec.Code)
		}
	}
	// and "" is magpie's own again, with nothing drawn over it
	if rec := lookServe(srv, "POST", "/api/settings", `{"theme":"light","lang":"en","accent":""}`); rec.Code != http.StatusOK || settings.Load().Accent != "" {
		t.Fatalf("accent not reset: %d %q", rec.Code, settings.Load().Accent)
	}
	if boot := lookServe(srv, "GET", "/boot.js", "").Body.String(); strings.Contains(boot, "lookCSS") || !strings.Contains(boot, `"look":","`) {
		t.Fatalf("boot.js still draws a look: %s", boot)
	}
}

// Whatever colour is picked, the accent drawn reads as text on the
// theme's background and cards, and the text on it reads too.
func TestAccentReadable(t *testing.T) {
	re := regexp.MustCompile(`--accent:(#[0-9a-f]{6});--accent-soft:(#[0-9a-f]{6});--accent-fg:(#[0-9a-f]{6});--sel:#[0-9a-f]{6};--mark:(#[0-9a-f]{6});--mark-fill:(#[0-9a-f]{6})$`)
	for _, pick := range []string{"#ffffff", "#000000", "#ffff00", "#00ff00", "#0d9488", "#2563eb", "#ea580c", "#52525b", "#808080", "#1a1a1e", "#f4f4f6"} {
		c, _ := parseHex(pick)
		for _, dark := range []bool{false, true} {
			m := re.FindStringSubmatch(accentVars(c, dark))
			if m == nil {
				t.Fatalf("%s: %s", pick, accentVars(c, dark))
			}
			a, _ := parseHex(m[1])
			fg, _ := parseHex(m[3])
			bgs := []rgb{lightBg, white}
			if dark {
				bgs = []rgb{darkBg, darkCard}
			}
			for _, bg := range bgs {
				if r := contrast(a, bg); r < 4.4 {
					t.Errorf("%s dark=%v: accent %s on %s is %.2f:1", pick, dark, m[1], bg.hex(), r)
				}
			}
			if r := contrast(a, fg); r < 4.4 {
				t.Errorf("%s dark=%v: text %s on accent %s is %.2f:1", pick, dark, m[3], m[1], r)
			}
			// what is chosen is in the colour picked (CHENGJIAHEGOXIS on X:
			// a colour picked showed nowhere on Settings), and reads as text
			// on every surface a chosen option sits on
			mark, _ := parseHex(m[4])
			// and on the tinted thumb under it (「主题深色依然还是灰色」)
			fill, _ := parseHex(m[5])
			on := []rgb{lightBg, white, lightPick, fill}
			if dark {
				on = []rgb{darkBg, darkCard, darkPick, fill}
			}
			for _, bg := range on {
				if r := contrast(mark, bg); r < 4.4 {
					t.Errorf("%s dark=%v: chosen ink %s on %s is %.2f:1", pick, dark, m[4], bg.hex(), r)
				}
			}
		}
	}
	// the chosen thumb is tinted with the colour in both themes, not left
	// grey (CHENGJIAHEGOXIS on X, 「主题深色依然还是灰色」)
	for _, dark := range []bool{false, true} {
		c, _ := parseHex("#7c3aed")
		m := re.FindStringSubmatch(accentVars(c, dark))
		f, _ := parseHex(m[5])
		if f[2]-f[1] < 12 {
			t.Errorf("dark=%v: the chosen thumb %s is still grey", dark, m[5])
		}
	}
	// a colour that reads already is drawn as picked
	c, _ := parseHex("#2563eb")
	if got := accentVars(c, false); !strings.HasPrefix(got, "--accent:#2563eb;") || !strings.Contains(got, ";--mark:#") {
		t.Fatalf("a readable blue was changed: %s", got)
	}
}

// A picture behind the pages is saved under magpie's folder, served back,
// drawn under a veil by boot.js, and removed with its file; what isn't a
// picture is refused.
func TestWallpaper(t *testing.T) {
	srv := appearanceHome(t)
	img := tinyPNG(t)
	body, _ := json.Marshal(map[string][]byte{"data": img})
	rec := lookServe(srv, "POST", "/api/appearance/wallpaper", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var got struct {
		Wallpaper int64 `json:"wallpaper"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &got); got.Wallpaper == 0 || settings.Load().Wallpaper != got.Wallpaper {
		t.Fatalf("answered %d, saved %d", got.Wallpaper, settings.Load().Wallpaper)
	}
	if b, err := os.ReadFile(settings.WallpaperPath()); err != nil || !bytes.Equal(b, img) {
		t.Fatalf("file: %v", err)
	}
	if !strings.HasPrefix(settings.WallpaperPath(), settings.Dir()) {
		t.Fatalf("%s is not under %s", settings.WallpaperPath(), settings.Dir())
	}
	if r := lookServe(srv, "GET", "/api/appearance/wallpaper", ""); r.Code != http.StatusOK || r.Header().Get("Content-Type") != "image/png" || !bytes.Equal(r.Body.Bytes(), img) {
		t.Fatalf("served %d %s", r.Code, r.Header().Get("Content-Type"))
	}
	boot := lookServe(srv, "GET", "/boot.js", "").Body.String()
	if !strings.Contains(boot, "/api/appearance/wallpaper?v=") || !strings.Contains(boot, "color-mix(in srgb,var(--bg) 84%") || !strings.Contains(boot, "body:not(.panel)") {
		t.Fatalf("boot.js draws no picture: %s", boot)
	}
	// /api/appearance/css says the same, for a page whose settings changed
	var css struct{ CSS, Key string }
	if json.Unmarshal(lookServe(srv, "GET", "/api/appearance/css", "").Body.Bytes(), &css); !strings.Contains(css.CSS, "/api/appearance/wallpaper?v=") || css.Key != lookKey(settings.Load()) {
		t.Fatalf("css: %+v", css)
	}
	// a save from Settings keeps it
	if rec := lookServe(srv, "POST", "/api/settings", `{"theme":"dark","lang":"en"}`); rec.Code != http.StatusOK || settings.Load().Wallpaper != got.Wallpaper {
		t.Fatalf("Settings' save dropped the picture: %d", rec.Code)
	}
	// not a picture, or nothing: refused, the picture kept
	for _, bad := range [][]byte{[]byte("<svg xmlns='http://www.w3.org/2000/svg'><script>alert(1)</script></svg>"), []byte("<html>hi</html>"), {}} {
		b, _ := json.Marshal(map[string][]byte{"data": bad})
		if rec := lookServe(srv, "POST", "/api/appearance/wallpaper", string(b)); rec.Code == http.StatusOK {
			t.Fatalf("%q was taken", bad)
		}
	}
	if b, _ := os.ReadFile(settings.WallpaperPath()); !bytes.Equal(b, img) || settings.Load().Wallpaper != got.Wallpaper {
		t.Fatal("a refused picture replaced the one saved")
	}
	// too big: refused before it is read whole
	big := append(append([]byte{}, img...), make([]byte, maxWallpaper)...)
	b, _ := json.Marshal(map[string][]byte{"data": big})
	if rec := lookServe(srv, "POST", "/api/appearance/wallpaper", string(b)); rec.Code == http.StatusOK {
		t.Fatal("a picture over 16 MB was taken")
	}
	// removed: the file goes, nothing is drawn
	if rec := lookServe(srv, "POST", "/api/appearance/wallpaper", `{"remove":true}`); rec.Code != http.StatusOK || settings.Load().Wallpaper != 0 {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(settings.WallpaperPath()); !os.IsNotExist(err) {
		t.Fatalf("the file stayed: %v", err)
	}
	if boot := lookServe(srv, "GET", "/boot.js", "").Body.String(); strings.Contains(boot, "wallpaper?v=") {
		t.Fatalf("boot.js still draws a picture: %s", boot)
	}
	if r := lookServe(srv, "GET", "/api/appearance/wallpaper", ""); r.Code != http.StatusNotFound {
		t.Fatalf("a removed picture is served: %d", r.Code)
	}
}

// A stamp whose file is gone (settings from another computer) draws no
// picture: the page would show its colour over nothing.
func TestWallpaperStampWithoutFile(t *testing.T) {
	appearanceHome(t)
	if css := lookCSS(settings.Settings{Wallpaper: 1}); css != "" {
		t.Fatalf("drawn without a file: %s", css)
	}
	if css := lookCSS(settings.Settings{}); css != "" {
		t.Fatalf("magpie's own look drew: %s", css)
	}
}

// A colour of the user's own tints the theme's grounds, not only what is
// chosen: the dark theme read as grey with a purple picked (CHENGJIAHEGOXIS
// on X, 「目前我个人感觉深色比较灰」, then 「依然没变」 on v0.1.4116 with the
// thumbs tinted). magpie's own look, and a grey pick, keep app.css's grey.
func TestAccentTintsGrounds(t *testing.T) {
	vars := func(css, sel string) map[string]string {
		i := strings.Index(css, sel+"{")
		if i < 0 {
			t.Fatalf("no %s in %s", sel, css)
		}
		body := css[i+len(sel)+1:]
		body = body[:strings.IndexByte(body, '}')]
		out := map[string]string{}
		for _, kv := range strings.Split(body, ";") {
			if k, v, ok := strings.Cut(kv, ":"); ok {
				out[k] = v
			}
		}
		return out
	}
	if css := lookCSS(settings.Settings{}); css != "" {
		t.Fatalf("magpie's own look draws %q", css)
	}
	css := lookCSS(settings.Settings{Accent: "#7c3aed"})
	for _, c := range []struct {
		sel      string
		surfaces []surface
	}{
		{`html:not(.omarchy):root`, lightSurfaces},
		{`html:not(.omarchy):root[data-theme="dark"]`, darkSurfaces},
		{`html:not(.omarchy):root:not([data-theme="light"])`, darkSurfaces},
		{`html:not(.omarchy):root[data-theme="light"] body.panel`, lightPanelSurfaces},
		{`html:not(.omarchy):root:not([data-theme="dark"]) body.panel`, lightPanelSurfaces},
		{`html:not(.omarchy):root[data-theme="dark"] body.panel`, darkPanelSurfaces},
		{`html:not(.omarchy):root:not([data-theme="light"]) body.panel`, darkPanelSurfaces},
	} {
		v := vars(css, c.sel)
		for _, s := range c.surfaces {
			got, ok := parseHex(v[s.name])
			if !ok {
				t.Fatalf("%s: no %s in %v", c.sel, s.name, v)
			}
			// purple: more blue than green, and about as light as before
			if got[2]-got[1] < 6 {
				t.Errorf("%s %s: %s is still grey (was %s)", c.sel, s.name, got.hex(), s.base.hex())
			}
			if r := contrast(got, s.base); r > 1.1 {
				t.Errorf("%s %s: %s is not as light as %s", c.sel, s.name, got.hex(), s.base.hex())
			}
		}
	}
	if !strings.Contains(vars(css, `html:not(.omarchy):root[data-theme="dark"]`)["--pop-bg"], "rgba(") {
		t.Errorf("the dark theme's menus keep their grey: %s", css)
	}
	// a grey pick leaves the grounds as they were
	g, _ := parseHex("#808080")
	for _, s := range darkSurfaces {
		if got := ground(s.base, g, true); got.hex() != s.base.hex() {
			t.Errorf("grey tints %s: %s", s.name, got.hex())
		}
	}
	// text and the accent read on the tinted grounds, for any pick
	for _, pick := range []string{"#ffffff", "#000000", "#ffff00", "#00ff00", "#ff0000", "#0000ff", "#7c3aed", "#0d9488", "#db2777"} {
		c, _ := parseHex(pick)
		for _, dark := range []bool{false, true} {
			surfaces, fg, accent := lightSurfaces, rgb{0x1c, 0x1c, 0x21}, accentOn(c, false)
			if dark {
				surfaces, fg, accent = darkSurfaces, rgb{0xed, 0xed, 0xf1}, accentOn(c, true)
			}
			for _, s := range surfaces {
				if strings.HasPrefix(s.name, "--line") || s.name == "--seg-track" {
					continue
				}
				bg := ground(s.base, c, dark)
				if r := contrast(fg, bg); r < 7 {
					t.Errorf("%s dark=%v: text on %s %s is %.2f:1", pick, dark, s.name, bg.hex(), r)
				}
				if s.name == "--bg" || s.name == "--card" {
					if r := contrast(accent, bg); r < 4.4 {
						t.Errorf("%s dark=%v: accent %s on %s %s is %.2f:1", pick, dark, accent.hex(), s.name, bg.hex(), r)
					}
				}
			}
		}
	}
}

// The grounds tinted are app.css's own, so a theme changed there is
// changed here too.
func TestTintBasesAreAppCSS(t *testing.T) {
	b, err := os.ReadFile("assets/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)
	block := func(sel string) string {
		i := strings.Index(css, sel+" {")
		if i < 0 {
			t.Fatalf("no %q in app.css", sel)
		}
		s := css[i:]
		return s[:strings.IndexByte(s, '}')]
	}
	for _, c := range []struct {
		sel      string
		surfaces []surface
	}{
		{":root", lightSurfaces},
		{`:root[data-theme="dark"]`, darkSurfaces},
		{`:root[data-theme="light"] body.panel`, lightPanelSurfaces},
		{`:root[data-theme="dark"] body.panel`, darkPanelSurfaces},
	} {
		blk := block(c.sel)
		for _, s := range c.surfaces {
			if !regexp.MustCompile(regexp.QuoteMeta(s.name) + `:\s*` + s.base.hex() + `;`).MatchString(blk) {
				t.Errorf("%s: app.css's %s is not %s", c.sel, s.name, s.base.hex())
			}
		}
	}
	if !strings.Contains(block(`:root[data-theme="dark"]`), "--pop-bg: rgba(38, 38, 43, .96)") {
		t.Errorf("app.css's dark --pop-bg is not rgba(38, 38, 43, .96)")
	}
}

// The GUI test's stand-in server hands the page lookCSS's own stylesheet
// for the colours it picks (tests/fixtures/look-accents.json). UPDATE=1
// writes it again.
func TestLookFixture(t *testing.T) {
	want := map[string]string{}
	for _, a := range []string{"#0d9488", "#db2777", "#7c3aed", "#000000", "#52525b", "#191047"} {
		want[a] = lookCSS(settings.Settings{Accent: a})
	}
	b, _ := json.MarshalIndent(want, "", "  ")
	b = append(b, '\n')
	const path = "tests/fixtures/look-accents.json"
	if os.Getenv("UPDATE") == "1" {
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, b) {
		t.Fatalf("%s is not lookCSS's (UPDATE=1 go test -run TestLookFixture writes it): %v", path, err)
	}
}

// Black, Graphite and magpie's own drew the same grey grounds, and black's
// ink was Graphite's grey (CHENGJIAHEGOXIS on X, 「这三个颜色……都显示是一样
// 的」, 「尤其是黑色始终一直是灰色」). Black now draws a black page on the
// dark theme, its cards, menus and lines the same steps above it, with a
// light ink of its own; on the light theme it is black ink. Graphite keeps
// the grey grounds with its grey ink, and magpie's own draws nothing.
func TestBlackIsBlack(t *testing.T) {
	blk, _ := parseHex("#000000")
	gra, _ := parseHex("#52525b")
	accent := func(c rgb, dark bool) rgb {
		m := regexp.MustCompile(`--accent:(#[0-9a-f]{6})`).FindStringSubmatch(accentVars(c, dark))
		a, _ := parseHex(m[1])
		return a
	}
	// the dark theme's page is black, and every ground keeps its step
	// above it, in the same order as app.css's
	if got := ground(darkBg, blk, true); got.hex() != "#000000" {
		t.Errorf("black's dark page is %s, not black", got.hex())
	}
	for _, set := range [][]surface{darkSurfaces, darkPanelSurfaces, {{"--pop-bg", darkPop}}} {
		for _, s := range set {
			got := ground(s.base, blk, true)
			if lb, lg := s.base.luminance(), got.luminance(); lg >= lb {
				t.Errorf("%s: black leaves %s at %s", s.name, s.base.hex(), got.hex())
			}
			want := (s.base[0]+s.base[1]+s.base[2])/3 - (darkBg[0]+darkBg[1]+darkBg[2])/3
			if g := (got[0] + got[1] + got[2]) / 3; math.Abs(g-max(0, want)) > 2 {
				t.Errorf("%s: %s is not its step %.0f above black", s.name, got.hex(), want)
			}
		}
	}
	// Graphite (lighter than the page) keeps the grounds' grey
	for _, s := range darkSurfaces {
		if got := ground(s.base, gra, true); got.hex() != s.base.hex() {
			t.Errorf("Graphite moves %s to %s", s.name, got.hex())
		}
	}
	// black's ink is not Graphite's grey: light on the dark theme, black
	// on the light one, each reading on its own grounds
	if a, g := accent(blk, true), accent(gra, true); contrast(a, g) < 1.8 || a.luminance() < 0.6 {
		t.Errorf("dark: black's accent %s is a grey like Graphite's %s", a.hex(), g.hex())
	}
	if a := accent(blk, false); a.hex() != "#000000" {
		t.Errorf("light: black's accent is %s", a.hex())
	}
	if r := contrast(accent(blk, true), ground(darkBg, blk, true)); r < 7 {
		t.Errorf("dark: black's accent on its page is %.2f:1", r)
	}
	// and the light theme's sibling: white's ink is dark, not a grey
	wht, _ := parseHex("#ffffff")
	if a := accent(wht, false); a.luminance() > 0.03 {
		t.Errorf("light: white's accent %s is a grey like Graphite's", a.hex())
	}
	// the page's text reads on black's grounds
	for _, s := range darkSurfaces {
		if r := contrast(rgb{0xed, 0xed, 0xf1}, ground(s.base, blk, true)); r < 7 {
			t.Errorf("%s: text on black's %s is %.2f:1", s.name, ground(s.base, blk, true).hex(), r)
		}
	}
}

// Black's page was #000003 with blue-grey cards, a blue-black beside
// Chrome's #000000, and the darkest navy of the system's picker drew the
// same grey page as Graphite, its mean channel no darker than the page's
// (CHENGJIAHEGOXIS on X: 「纯黑……感觉色对不上」, 「感觉是有浅灰存在，所以
// 导致深变浅」). Black's grounds are neutral greys over #000000, a dark
// colour's page goes down toward black in its own hue, and Graphite and
// the presets keep magpie's grounds.
func TestDarkPickDarkensPage(t *testing.T) {
	blk, _ := parseHex("#000000")
	for _, set := range [][]surface{darkSurfaces, darkPanelSurfaces, {{"--pop-bg", darkPop}}} {
		for _, s := range set {
			if got := ground(s.base, blk, true); got.hex()[1:3] != got.hex()[3:5] || got.hex()[3:5] != got.hex()[5:7] {
				t.Errorf("black's %s is %s, not a neutral grey", s.name, got.hex())
			}
		}
	}
	for _, pick := range []string{"#191047", "#130830", "#0c1a3a"} {
		c, _ := parseHex(pick)
		bg := ground(darkBg, c, true)
		if lightness(bg) > lightness(darkBg)-0.05 {
			t.Errorf("%s: its page %s is not darker than magpie's %s", pick, bg.hex(), darkBg.hex())
		}
		if bg[2]-bg[0] < 8 {
			t.Errorf("%s: its page %s is not the colour's blue", pick, bg.hex())
		}
		// the page's text and the accent read on every ground
		for _, s := range darkSurfaces {
			if strings.HasPrefix(s.name, "--line") {
				continue
			}
			g := ground(s.base, c, true)
			if r := contrast(rgb{0xed, 0xed, 0xf1}, g); r < 7 {
				t.Errorf("%s: text on %s %s is %.2f:1", pick, s.name, g.hex(), r)
			}
		}
		if r := contrast(accentOn(c, true), bg); r < 4.4 {
			t.Errorf("%s: accent on its page is %.2f:1", pick, r)
		}
	}
	for _, pick := range []string{"#52525b", "#4f46e5", "#2563eb", "#0d9488", "#16a34a", "#ea580c", "#db2777", "#0000ff"} {
		c, _ := parseHex(pick)
		if d := darkness(c); d != 0 {
			t.Errorf("%s darkens the page by %.2f", pick, d)
		}
	}
}
