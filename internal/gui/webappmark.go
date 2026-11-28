package gui

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// magpie web's icons in the accent of the user's own (eachg0 on X: several
// magpies added to the Dock, each named in its tab's title, all had the one
// black bird). With Settings › General's Accent color picked, the tab's
// favicon, the apple-touch-icon and the manifest's icons draw the bird in
// that colour, and the manifest's and the page's theme colours are the
// window's ground as the accent tints it. With no accent, or under
// Omarchy, whose theme is the look there, each is the build's own file,
// served as before.

// markAccent is the colour this magpie's icons are drawn in, and whether
// it has one of its own.
func markAccent(s settings.Settings) (rgb, bool) {
	c, ok := parseHex(strings.ToLower(s.Accent))
	if !ok {
		return rgb{}, false
	}
	if _, on := omarchyTheme(); on {
		return rgb{}, false
	}
	return c, true
}

// appMark is what the page names for its icons and window colours: the
// tab's favicon, the apple-touch-icon and the standalone window's theme
// colours, light and dark. boot.js puts it in the page before it paints,
// and /api/appearance/css hands it to a page whose accent changed
// (appearance.js markApp).
type appMark struct {
	Icon  string `json:"icon"`
	Touch string `json:"touch"`
	Light string `json:"light"`
	Dark  string `json:"dark"`
}

// defaultMark is index.html's own.
var defaultMark = appMark{Icon: "icons/favicon.svg", Touch: "icons/apple-touch-icon.png", Light: "#f4f4f6", Dark: "#1a1a1e"}

// markOf is s's appMark. The icons' addresses name the colour, so a
// browser that kept the old picture asks for the new one; the files
// themselves are drawn in the saved accent whatever the address says.
func markOf(s settings.Settings) appMark {
	c, ok := markAccent(s)
	if !ok {
		return defaultMark
	}
	q := "?accent=" + strings.TrimPrefix(c.hex(), "#")
	return appMark{
		Icon:  defaultMark.Icon + q,
		Touch: defaultMark.Touch + q,
		Light: ground(lightBg, c, false).hex(),
		Dark:  ground(darkBg, c, true).hex(),
	}
}

// markBoot is boot.js's line that puts m in the page, or nothing for
// index.html's own.
func markBoot(m appMark) string {
	if m == defaultMark {
		return ""
	}
	b, _ := json.Marshal(m)
	return `(function(m){var h=document.head,q=function(s){return h.querySelector(s)||{}};` +
		`q('link[rel="icon"]').href=m.icon;q('link[rel="apple-touch-icon"]').href=m.touch;` +
		`q('meta[name="theme-color"][media*="light"]').content=m.light;q('meta[name="theme-color"][media*="dark"]').content=m.dark})(` +
		string(b) + ");\n"
}

// markInk is the bird's colour on the icons' light ground: the accent,
// darkened as on the light theme until it reads there.
func markInk(c rgb) rgb { return accentOn(c, false) }

// markSVG is the favicon with its bird in c: the light theme's ink on a
// light tab strip, the dark theme's on a dark one. ok is false when the
// file no longer has the fills it is drawn from.
func markSVG(src []byte, c rgb) ([]byte, bool) {
	const light, dark = "path{fill:#1c1c1e}", "path{fill:#f2f2f2}"
	s := string(src)
	if !strings.Contains(s, light) || !strings.Contains(s, dark) {
		return nil, false
	}
	s = strings.Replace(s, light, "path{fill:"+markInk(c).hex()+"}", 1)
	s = strings.Replace(s, dark, "path{fill:"+accentOn(c, true).hex()+"}", 1)
	return []byte(s), true
}

// markPNG is the icon with its ink in c. The icon is a black bird on a
// near-white ground: each pixel's grey says how much of the ground it is,
// so it is drawn as the ink mixed that far toward white, the edges' blend
// kept and the white of the bird left white.
func markPNG(src []byte, c rgb) ([]byte, error) {
	img, err := png.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, err
	}
	ink := markInk(c)
	b := img.Bounds()
	out := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			p := color.RGBAModel.Convert(img.At(x, y)).(color.RGBA)
			g := (float64(p.R) + float64(p.G) + float64(p.B)) / 3 / 255
			m := ink.mix(white, g)
			out.SetRGBA(x, y, color.RGBA{uint8(m[0] + 0.5), uint8(m[1] + 0.5), uint8(m[2] + 0.5), p.A})
		}
	}
	var w bytes.Buffer
	if err := png.Encode(&w, out); err != nil {
		return nil, err
	}
	return w.Bytes(), nil
}

// markedIcons keeps the icons drawn last, by file and colour: the accent
// changes rarely, and the files are asked for without the key. A dev
// build reads its files from disk, as they change, and keeps none.
var markedIcons struct {
	sync.Mutex
	m map[string][]byte
}

func markedFile(name string, c rgb) ([]byte, bool) {
	key := name + c.hex()
	markedIcons.Lock()
	b, hit := markedIcons.m[key]
	markedIcons.Unlock()
	if hit {
		return b, true
	}
	src, err := fs.ReadFile(staticFS(), name)
	if err != nil {
		return nil, false
	}
	ok := true
	if strings.HasSuffix(name, ".svg") {
		b, ok = markSVG(src, c)
	} else if b, err = markPNG(src, c); err != nil {
		ok = false
	}
	if !ok {
		return nil, false
	}
	if !staticFixed {
		return b, true
	}
	markedIcons.Lock()
	if markedIcons.m == nil || len(markedIcons.m) >= 8 {
		markedIcons.m = map[string][]byte{}
	}
	markedIcons.m[key] = b
	markedIcons.Unlock()
	return b, true
}

// markedManifest is manifest.json with the icons' addresses and the
// window's colours of m.
func markedManifest(m appMark) ([]byte, bool) {
	src, err := fs.ReadFile(staticFS(), "manifest.json")
	if err != nil {
		return nil, false
	}
	var mf map[string]any
	if json.Unmarshal(src, &mf) != nil {
		return nil, false
	}
	q := m.Icon[strings.IndexByte(m.Icon, '?'):]
	icons, _ := mf["icons"].([]any)
	for _, ic := range icons {
		if o, ok := ic.(map[string]any); ok {
			if s, ok := o["src"].(string); ok {
				o["src"] = s + q
			}
		}
	}
	mf["theme_color"], mf["background_color"] = m.Light, m.Light
	b, err := json.MarshalIndent(mf, "", "  ")
	return b, err == nil
}

// markRoutes serve the favicon, the home screen's icons and the manifest
// in the saved accent, and page's own files without one.
func markRoutes(mux *http.ServeMux, page http.Handler) {
	files := map[string]string{
		"/icons/favicon.svg":          "image/svg+xml",
		"/icons/apple-touch-icon.png": "image/png",
		"/icons/icon-512.png":         "image/png",
		"/manifest.json":              "application/json",
	}
	for p, ct := range files {
		mux.HandleFunc("GET "+p, func(rw http.ResponseWriter, r *http.Request) {
			s := settings.Load()
			c, ok := markAccent(s)
			var b []byte
			if ok {
				if p == "/manifest.json" {
					b, ok = markedManifest(markOf(s))
				} else {
					b, ok = markedFile(strings.TrimPrefix(p, "/"), c)
				}
			}
			if !ok {
				page.ServeHTTP(rw, r)
				return
			}
			rw.Header().Set("Content-Type", ct)
			rw.Header().Set("Cache-Control", "no-cache")
			rw.Header().Set("ETag", `"`+strings.TrimPrefix(c.hex(), "#")+"-"+Version+`"`)
			http.ServeContent(rw, r, "", time.Time{}, bytes.NewReader(b))
		})
	}
}
