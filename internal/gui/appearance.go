package gui

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/settings"
)

// Settings › General's Accent color and Background image
// (CHENGJIAHEGOXIS on X): a colour of the user's own for magpie's
// buttons, links and selection, and a picture of their own behind the
// window's pages. Both are drawn by lookCSS, a stylesheet boot.js puts in
// the page before it paints and /api/appearance/css hands a page whose
// settings changed (appearance.js).

// maxWallpaper is the largest picture taken.
const maxWallpaper = 16 << 20

// wallpaperTypes are the pictures every webview magpie runs in draws.
var wallpaperTypes = []string{"image/png", "image/jpeg", "image/gif", "image/webp"}

// wallpaperType is b's type when it is a picture magpie takes.
func wallpaperType(b []byte) (string, error) {
	if len(b) == 0 || len(b) > maxWallpaper {
		return "", fmt.Errorf("a background image is %d MB at most", maxWallpaper>>20)
	}
	ct := http.DetectContentType(b)
	for _, t := range wallpaperTypes {
		if ct == t {
			return ct, nil
		}
	}
	return "", errors.New("a background image is a PNG, JPEG, GIF or WebP picture")
}

// lookKey is what the page compares to know its look is the saved one,
// as app.js's applyPrefs writes it: the accent and the wallpaper's stamp.
func lookKey(s settings.Settings) string {
	w := ""
	if s.Wallpaper != 0 {
		w = strconv.FormatInt(s.Wallpaper, 10)
	}
	return s.Accent + "," + w
}

// lookCSS is the stylesheet that draws s's accent and wallpaper, "" for
// magpie's own look. It never applies under Omarchy, whose theme is the
// look there.
func lookCSS(s settings.Settings) string {
	var b strings.Builder
	if c, ok := parseHex(s.Accent); ok {
		light, dark := accentVars(c, false), accentVars(c, true)
		fmt.Fprintf(&b, "html:not(.omarchy):root{%s}\n", light)
		fmt.Fprintf(&b, "@media (prefers-color-scheme: dark){html:not(.omarchy):root:not([data-theme=\"light\"]){%s}}\n", dark)
		fmt.Fprintf(&b, "html:not(.omarchy):root[data-theme=\"dark\"]{%s}\n", dark)
	}
	if s.Wallpaper != 0 {
		if fi, err := os.Stat(settings.WallpaperPath()); err == nil && fi.Mode().IsRegular() {
			// the picture fills the window behind the pages, under a veil of
			// the theme's own background (one colour, the top layer), so text
			// on it reads as on the plain page; the cards over it stay opaque.
			// The background's colour is still the theme's, which Windows'
			// title bar is painted from (tintTitleBar)
			fmt.Fprintf(&b, "html:not(.omarchy) body:not(.panel){background:linear-gradient(%[1]s,%[1]s),url(\"/api/appearance/wallpaper?v=%[2]d\") center/cover no-repeat var(--bg)}\n", wallVeil, s.Wallpaper)
			b.WriteString("html:not(.omarchy) body:not(.panel) .sidebar{background:transparent}\n")
		}
	}
	return b.String()
}

type rgb [3]float64

func parseHex(s string) (rgb, bool) {
	if len(s) != 7 || s[0] != '#' {
		return rgb{}, false
	}
	n, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return rgb{}, false
	}
	return rgb{float64(n >> 16 & 255), float64(n >> 8 & 255), float64(n & 255)}, true
}

func (c rgb) hex() string {
	return fmt.Sprintf("#%02x%02x%02x", int(math.Round(c[0])), int(math.Round(c[1])), int(math.Round(c[2])))
}

// mix is c moved p of the way to o.
func (c rgb) mix(o rgb, p float64) rgb {
	return rgb{c[0] + (o[0]-c[0])*p, c[1] + (o[1]-c[1])*p, c[2] + (o[2]-c[2])*p}
}

// luminance is WCAG's relative luminance.
func (c rgb) luminance() float64 {
	ch := func(v float64) float64 {
		v /= 255
		if v <= 0.03928 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(c[0]) + 0.7152*ch(c[1]) + 0.0722*ch(c[2])
}

// contrast is WCAG's contrast ratio of a and b.
func contrast(a, b rgb) float64 {
	la, lb := a.luminance(), b.luminance()
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

var (
	white = rgb{255, 255, 255}
	black = rgb{0, 0, 0}
	// the themes' backgrounds (app.css --bg and --card), what the accent
	// is read against as text
	lightBg   = rgb{0xf4, 0xf4, 0xf6}
	darkBg    = rgb{0x1a, 0x1a, 0x1e}
	darkCard  = rgb{0x23, 0x23, 0x27}
	accentInk = 4.5 // WCAG AA for text: the accent is a link's colour too
	// how much of the theme's background lies over the picture
	wallVeil = "color-mix(in srgb,var(--bg) 84%,transparent)"
)

// accentOn is c, darkened (light theme) or lightened (dark theme) just
// enough to read as text on that theme's background and cards.
func accentOn(c rgb, dark bool) rgb {
	toward, against := black, lightBg
	if dark {
		toward, against = white, darkCard
	}
	out := c
	for p := 0.0; p <= 1 && contrast(out, against) < accentInk; p += 0.02 {
		out = c.mix(toward, p)
	}
	return out
}

// accentVars are the theme's --accent, its soft fill, the text on it and
// the selection, from c.
func accentVars(c rgb, dark bool) string {
	a := accentOn(c, dark)
	fg := white
	if contrast(a, rgb{0x14, 0x14, 0x1a}) > contrast(a, white) {
		fg = rgb{0x14, 0x14, 0x1a}
	}
	soft, sel := white.mix(a, 0.09), white.mix(a, 0.11)
	if dark {
		soft, sel = darkBg.mix(a, 0.14), darkBg.mix(a, 0.2)
	}
	return fmt.Sprintf("--accent:%s;--accent-soft:%s;--accent-fg:%s;--sel:%s", a.hex(), soft.hex(), fg.hex(), sel.hex())
}

// appearanceRoutes serve the look's stylesheet and the wallpaper, and take
// a picture chosen or removed.
func appearanceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/appearance/css", func(rw http.ResponseWriter, r *http.Request) {
		s := settings.Load()
		writeJSON(rw, map[string]string{"css": lookCSS(s), "key": lookKey(s)})
	})
	mux.HandleFunc("GET /api/appearance/wallpaper", func(rw http.ResponseWriter, r *http.Request) {
		b, err := os.ReadFile(settings.WallpaperPath())
		if err != nil {
			http.NotFound(rw, r)
			return
		}
		ct, err := wallpaperType(b)
		if err != nil {
			http.NotFound(rw, r)
			return
		}
		rw.Header().Set("Content-Type", ct)
		rw.Header().Set("Cache-Control", "private, max-age=31536000, immutable") // its address changes with it (?v=)
		rw.Header().Set("X-Content-Type-Options", "nosniff")
		rw.Write(b)
	})
	// the picture comes as base64 in JSON, as the app's web view drops a
	// File sent as the body; {"remove": true} takes it away
	mux.HandleFunc("POST /api/appearance/wallpaper", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Data   []byte `json:"data"`
			Remove bool   `json:"remove"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, maxWallpaper*4/3+64<<10)).Decode(&in); err != nil {
			fail(rw, fmt.Errorf("a background image is a PNG, JPEG, GIF or WebP picture of %d MB at most", maxWallpaper>>20))
			return
		}
		if err := setWallpaper(in.Data, in.Remove); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
}

// setWallpaper saves b as the picture behind the pages, or removes it.
func setWallpaper(b []byte, remove bool) error {
	if !remove {
		if _, err := wallpaperType(b); err != nil {
			return err
		}
	}
	s, err := settings.LoadChecked()
	if err != nil {
		return err
	}
	if remove {
		s.Wallpaper = 0
		if err := settings.Save(s); err != nil {
			return err
		}
		if err := os.Remove(settings.WallpaperPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		return err
	}
	if err := edit.WriteAtomic(settings.WallpaperPath(), b); err != nil {
		return err
	}
	// a new stamp, so the page asks for the new picture, not its cached one
	s.Wallpaper = max(time.Now().UnixMilli(), s.Wallpaper+1)
	return settings.Save(s)
}
