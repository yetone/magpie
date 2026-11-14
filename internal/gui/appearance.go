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
		light := accentVars(c, false) + ";" + tintVars(c, lightSurfaces, false)
		dark := accentVars(c, true) + ";" + tintVars(c, darkSurfaces, true) + ";" + popVar(c)
		fmt.Fprintf(&b, "html:not(.omarchy):root{%s}\n", light)
		fmt.Fprintf(&b, "@media (prefers-color-scheme: dark){html:not(.omarchy):root:not([data-theme=\"light\"]){%s}}\n", dark)
		fmt.Fprintf(&b, "html:not(.omarchy):root[data-theme=\"dark\"]{%s}\n", dark)
		// the tray panel draws its surfaces on body.panel, so they are
		// tinted there too, the frosted panel's tint (tintPanel) with them
		lightP, darkP := tintVars(c, lightPanelSurfaces, false), tintVars(c, darkPanelSurfaces, true)
		fmt.Fprintf(&b, "@media (prefers-color-scheme: light){html:not(.omarchy):root:not([data-theme=\"dark\"]) body.panel{%s}}\n", lightP)
		fmt.Fprintf(&b, "html:not(.omarchy):root[data-theme=\"light\"] body.panel{%s}\n", lightP)
		fmt.Fprintf(&b, "@media (prefers-color-scheme: dark){html:not(.omarchy):root:not([data-theme=\"light\"]) body.panel{%s}}\n", darkP)
		fmt.Fprintf(&b, "html:not(.omarchy):root[data-theme=\"dark\"] body.panel{%s}\n", darkP)
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
	// the surfaces a chosen option's text sits on (--mark): the light
	// theme's darkest, a tab's --pill-hover, and the dark theme's lightest,
	// the header switch's thumb (--seg-thumb over --seg-track)
	lightPick = rgb{0xe9, 0xe9, 0xee}
	darkPick  = rgb{0x4c, 0x4c, 0x52}
	// the dark theme's chosen thumb before it is tinted (--mark-fill): the
	// header switch's thumb as it reads over the page
	darkFill = rgb{0x3a, 0x3a, 0x3f}
	// how much of the theme's background lies over the picture
	wallVeil = "color-mix(in srgb,var(--bg) 84%,transparent)"
)

// accentOn is c, darkened (light theme) or lightened (dark theme) just
// enough to read as text on that theme's background and cards, grey or
// tinted with c.
func accentOn(c rgb, dark bool) rgb {
	if dark {
		return inkOn(lifted(c, true), white, darkCard, ground(darkCard, c, true), ground(darkBg, c, true))
	}
	return inkOn(lifted(c, false), black, lightBg, ground(lightBg, c, false))
}

// inkOn is c moved toward toward just enough to read as text on each of
// against.
func inkOn(c, toward rgb, against ...rgb) rgb {
	reads := func(o rgb) bool {
		for _, bg := range against {
			if contrast(o, bg) < accentInk {
				return false
			}
		}
		return true
	}
	out := c
	for p := 0.0; p <= 1 && !reads(out); p += 0.02 {
		out = c.mix(toward, p)
	}
	return out
}

// accentVars are the theme's --accent, its soft fill, the text on it, the
// selection, --mark and --mark-fill, from c. --mark is the ink of what is
// chosen (the open page's sidebar icon, a switch's or tab's chosen
// option), magpie's own text colour without an accent of the user's, as a
// Mac's Graphite: without it a colour picked showed nowhere on Settings,
// nor on most pages (CHENGJIAHEGOXIS on X, 「选中任意强调色没反应」).
// --mark-fill is the ground under it, a segmented control's thumb or a
// chosen tab's, tinted with the colour: a grey thumb under a coloured word
// still read as grey in the dark theme (「主题深色依然还是灰色」). It is
// unset without an accent, so each keeps its own grey there.
func accentVars(c rgb, dark bool) string {
	a := accentOn(c, dark)
	fg := white
	if contrast(a, rgb{0x14, 0x14, 0x1a}) > contrast(a, white) {
		fg = rgb{0x14, 0x14, 0x1a}
	}
	soft, sel := white.mix(a, 0.09), white.mix(a, 0.11)
	fill := white.mix(a, 0.12)
	mark := inkOn(lifted(c, false), black, lightPick, ground(lightPick, c, false), fill)
	if dark {
		bg := ground(darkBg, c, true)
		soft, sel = bg.mix(a, 0.14), bg.mix(a, 0.2)
		fill = ground(darkFill, c, true).mix(a, 0.26)
		mark = inkOn(lifted(c, true), white, darkPick, ground(darkPick, c, true), fill)
	}
	return fmt.Sprintf("--accent:%s;--accent-soft:%s;--accent-fg:%s;--sel:%s;--mark:%s;--mark-fill:%s", a.hex(), soft.hex(), fg.hex(), sel.hex(), mark.hex(), fill.hex())
}

// surface is one of a theme's grounds, as app.css draws it without an
// accent: its token and colour.
type surface struct {
	name string
	base rgb
}

// The themes' grounds (app.css :root and its dark blocks, and body.panel's
// own in each theme). The light theme's cards stay white, as on a Mac.
var (
	lightSurfaces = []surface{{"--bg", lightBg}, {"--card-2", rgb{0xf9, 0xf9, 0xfb}}, {"--pill", rgb{0xf1, 0xf1, 0xf4}}, {"--pill-hover", lightPick}, {"--seg-track", rgb{0xe6, 0xe6, 0xeb}}, {"--line", rgb{0xe3, 0xe3, 0xe8}}, {"--line-2", rgb{0xec, 0xec, 0xf0}}}
	darkSurfaces  = []surface{{"--bg", darkBg}, {"--card", darkCard}, {"--card-2", rgb{0x1f, 0x1f, 0x23}}, {"--pill", rgb{0x2d, 0x2d, 0x33}}, {"--pill-hover", rgb{0x36, 0x36, 0x3d}}, {"--line", rgb{0x33, 0x33, 0x39}}, {"--line-2", rgb{0x2c, 0x2c, 0x32}}}
	// --pop-bg, a menu's ground, is translucent: rgba(38, 38, 43, .96)
	darkPop            = rgb{38, 38, 43}
	lightPanelSurfaces = []surface{{"--bg", rgb{0xf2, 0xf2, 0xf5}}, {"--card-2", rgb{0xf8, 0xf8, 0xfa}}, {"--pill-hover", rgb{0xf4, 0xf4, 0xf7}}, {"--skel", rgb{0xee, 0xee, 0xf2}}, {"--skel-hi", rgb{0xe2, 0xe2, 0xe8}}}
	darkPanelSurfaces  = []surface{{"--bg", rgb{0x1b, 0x1c, 0x20}}, {"--card", rgb{0x25, 0x26, 0x2c}}, {"--card-2", rgb{0x21, 0x22, 0x27}}, {"--pill", rgb{0x2e, 0x2f, 0x36}}, {"--pill-hover", rgb{0x38, 0x39, 0x42}}}
	// how much of the colour's hue the grounds take: enough that the dark
	// theme reads as the colour's, not grey (CHENGJIAHEGOXIS on X, 「目前我
	// 个人感觉深色比较灰」), and only a breath of it on the light theme
	lightTint = 0.05
	darkTint  = 0.08
)

// tint is base with k of c's hue added: c less its own grey, so the ground
// keeps its lightness (text on it reads as before) and a grey pick leaves
// it as it was.
func tint(base, c rgb, k float64) rgb {
	g := (c[0] + c[1] + c[2]) / 3
	var out rgb
	for i := range out {
		out[i] = min(255, max(0, base[i]+(c[i]-g)*k))
	}
	return out
}

// depth is how far past the dark theme's background c is toward black,
// 0 for a colour as light as it or lighter, 1 for black, where lifted
// starts its ink (CHENGJIAHEGOXIS on X: black's ink was Graphite's grey,
// 「尤其是黑色始终一直是灰色」). On the light theme, the same for a pick
// lighter than its background, toward white. The dark grounds go down by
// darkness, which reads a colour's lightness as the eye does.
func depth(c rgb, dark bool) float64 {
	g := (c[0] + c[1] + c[2]) / 3
	if dark {
		ref := (darkBg[0] + darkBg[1] + darkBg[2]) / 3
		return max(0, ref-g) / ref
	}
	ref := (lightBg[0] + lightBg[1] + lightBg[2]) / 3
	return max(0, g-ref) / (255 - ref)
}

// lightness is c's OKLab lightness, 0 for black and 1 for white: how
// light a colour looks, whatever its hue. A deep navy's mean channel is
// as high as the dark theme's grey, but it looks far darker than it.
func lightness(c rgb) float64 {
	lin := func(v float64) float64 {
		v /= 255
		if v <= 0.04045 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	r, g, b := lin(c[0]), lin(c[1]), lin(c[2])
	l := math.Cbrt(0.4122214708*r + 0.5363325363*g + 0.0514459929*b)
	m := math.Cbrt(0.2119034982*r + 0.6806995451*g + 0.1073969566*b)
	s := math.Cbrt(0.0883024619*r + 0.2817188376*g + 0.6299787005*b)
	return 0.2104542553*l + 0.7936177850*m - 0.0040720468*s
}

// darkLight is the lightness under which a pick is a dark colour, one
// the dark theme's grounds go down toward: Graphite (#52525b) and every
// preset are lighter, a deep navy or a near black are darker.
const darkLight = 0.4

// darkness is how far c is toward black past darkLight: 0 for Graphite
// or any lighter colour, 1 for black (CHENGJIAHEGOXIS on X: the darkest
// navy of the system's picker drew the same grey page as Graphite, 「感觉
// 是有浅灰存在，所以导致深变浅」).
func darkness(c rgb) float64 {
	return max(0, darkLight-lightness(c)) / darkLight
}

// ground is one of the theme's grounds once c is picked: tinted with c's
// hue, and, on the dark theme, as much darker as c is a dark colour
// (darkness), so black draws a black page with its cards, menus and
// lines the same steps above it, and a deep navy a near-black navy one.
// As it goes down the ground loses app.css's own cool grey, so black's
// page is #000000, Chrome's and the system's black, not a blue-black
// (「纯黑……感觉色对不上」), and the pick's hue counts for more, so a dark
// colour's page is that colour's and not grey. The light theme's grounds
// keep their lightness: a white pick there would melt its white cards
// into the page.
func ground(base, c rgb, dark bool) rgb {
	if !dark {
		return tint(base, c, lightTint)
	}
	d := darkness(c)
	mean := (base[0] + base[1] + base[2]) / 3
	out := tint(base.mix(rgb{mean, mean, mean}, d), c, darkTint+d*0.25)
	step := d * (darkBg[0] + darkBg[1] + darkBg[2]) / 3
	for i := range out {
		out[i] = max(0, out[i]-step)
	}
	return out
}

// lifted is where c's ink starts on a theme: c itself, or, for a pick
// past the theme's background (black on the dark theme, white on the
// light one), most of the way to the other end, so black reads as the
// dark theme's light ink and not as a grey that is Graphite's (and white
// as the light theme's dark ink). A colour that reads as it is starts
// as it is.
func lifted(c rgb, dark bool) rgb {
	if dark {
		return c.mix(white, 0.88*depth(c, true))
	}
	return c.mix(black, 0.88*depth(c, false))
}

// tintVars are surfaces as ground draws them, the theme's grounds once
// the user picked a colour of their own. Without one each keeps app.css's
// grey.
func tintVars(c rgb, surfaces []surface, dark bool) string {
	parts := make([]string, 0, len(surfaces))
	for _, s := range surfaces {
		parts = append(parts, s.name+":"+ground(s.base, c, dark).hex())
	}
	return strings.Join(parts, ";")
}

// popVar is the dark theme's menu ground, tinted as its other grounds are.
func popVar(c rgb) string {
	p := ground(darkPop, c, true)
	return fmt.Sprintf("--pop-bg:rgba(%d,%d,%d,.96)", int(math.Round(p[0])), int(math.Round(p[1])), int(math.Round(p[2])))
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
