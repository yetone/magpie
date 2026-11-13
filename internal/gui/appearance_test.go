package gui

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
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
	re := regexp.MustCompile(`--accent:(#[0-9a-f]{6});--accent-soft:(#[0-9a-f]{6});--accent-fg:(#[0-9a-f]{6})`)
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
		}
	}
	// a colour that reads already is drawn as picked
	c, _ := parseHex("#2563eb")
	if got := accentVars(c, false); !strings.HasPrefix(got, "--accent:#2563eb;") {
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
