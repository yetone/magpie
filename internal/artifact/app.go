package artifact

import (
	"bytes"
	"encoding/json"
	"hash/fnv"
	"image"
	"image/color"
	pngenc "image/png"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// An artifact installs as an app from the browser it opens in: its page's
// folder is the app's scope, with a manifest, an icon drawn from its
// title, and a service worker that answers when magpie isn't running
// (docs/subsystems/artifacts.md, "Install as app").
const (
	AppManifest = "app.webmanifest"
	AppWorker   = "app-sw.js"
	// AppIcon is the icon's name before its size: app-icon-192.png
	AppIcon = "app-icon-"
)

// AppIconSizes are the sizes the manifest lists, which browsers pick from.
var AppIconSizes = []int{192, 512}

// Manifest is the web app manifest of the artifact at home, its page's
// folder (/a/<id>/); api is that folder's _api, where the icons are.
func Manifest(a *Artifact, home, api string) []byte {
	name := strings.TrimSpace(a.Title)
	if name == "" {
		name = a.ID
	}
	type icon struct {
		Src     string `json:"src"`
		Sizes   string `json:"sizes"`
		Type    string `json:"type"`
		Purpose string `json:"purpose"`
	}
	m := struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		ShortName  string `json:"short_name"`
		StartURL   string `json:"start_url"`
		Scope      string `json:"scope"`
		Display    string `json:"display"`
		Background string `json:"background_color"`
		Icons      []icon `json:"icons"`
	}{ID: home, Name: name, ShortName: shortName(name), StartURL: home, Scope: home, Display: "standalone", Background: "#ffffff"}
	for _, s := range AppIconSizes {
		n := strconv.Itoa(s)
		m.Icons = append(m.Icons, icon{Src: api + "/" + AppIcon + n + ".png", Sizes: n + "x" + n, Type: "image/png", Purpose: "any"})
	}
	b, _ := json.Marshal(m)
	return b
}

// shortName is name cut to what a launcher shows under an icon.
func shortName(name string) string {
	if utf8.RuneCountInString(name) <= 24 {
		return name
	}
	r := []rune(name)
	return strings.TrimSpace(string(r[:23])) + "…"
}

// appColors are the icons' backgrounds, quiet and each with white on it
// readable; an artifact keeps its own, from its id.
var appColors = []color.RGBA{
	{0x4a, 0x5d, 0x8f, 0xff}, {0x3f, 0x7a, 0x6b, 0xff}, {0x9a, 0x5a, 0x44, 0xff}, {0x5f, 0x63, 0x6e, 0xff},
	{0x74, 0x5c, 0x9a, 0xff}, {0x3b, 0x6f, 0xa3, 0xff}, {0x86, 0x6a, 0x3a, 0xff}, {0x4b, 0x7a, 0x50, 0xff},
}

// AppIconPNG draws the artifact's icon at size: a rounded square in its
// colour, on the platform's icon grid (a margin around it, as macOS's
// icons have), with the title's first letter, or a page where the font
// has no such letter.
func AppIconPNG(a *Artifact, size int) ([]byte, error) {
	h := fnv.New32a()
	h.Write([]byte(a.ID))
	bg := appColors[h.Sum32()%uint32(len(appColors))]

	img := image.NewRGBA(image.Rect(0, 0, size, size))
	s := float32(size)
	inset, side := s*0.1, s*0.8
	r := side * 0.225
	z := vector.NewRasterizer(size, size)
	roundRect(z, inset, inset, side, side, r)
	z.Draw(img, img.Bounds(), image.NewUniform(bg), image.Point{})

	if !drawInitial(img, a.Title, size) {
		// a page: a white sheet with a folded corner's lines
		z = vector.NewRasterizer(size, size)
		roundRect(z, s*0.36, s*0.31, s*0.28, s*0.38, s*0.03)
		z.Draw(img, img.Bounds(), image.NewUniform(color.White), image.Point{})
		z = vector.NewRasterizer(size, size)
		for i := 0; i < 3; i++ {
			roundRect(z, s*0.41, s*(0.40+float32(i)*0.08), s*0.18, s*0.025, s*0.0125)
		}
		z.Draw(img, img.Bounds(), image.NewUniform(bg), image.Point{})
	}
	var b bytes.Buffer
	if err := pngenc.Encode(&b, img); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// roundRect adds a rounded rectangle to z, its corners as cubic arcs.
func roundRect(z *vector.Rasterizer, x, y, w, h, r float32) {
	k := r * 0.5523 // a quarter circle's control distance
	z.MoveTo(x+r, y)
	z.LineTo(x+w-r, y)
	z.CubeTo(x+w-r+k, y, x+w, y+r-k, x+w, y+r)
	z.LineTo(x+w, y+h-r)
	z.CubeTo(x+w, y+h-r+k, x+w-r+k, y+h, x+w-r, y+h)
	z.LineTo(x+r, y+h)
	z.CubeTo(x+r-k, y+h, x, y+h-r+k, x, y+h-r)
	z.LineTo(x, y+r)
	z.CubeTo(x, y+r-k, x+r-k, y, x+r, y)
	z.ClosePath()
}

var appFont, _ = opentype.Parse(gobold.TTF)

// drawInitial draws title's first letter or digit, upper-cased, centred
// in white; false when there is none the font has.
func drawInitial(img *image.RGBA, title string, size int) bool {
	var c rune
	for _, r := range title {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			c = unicode.ToUpper(r)
			break
		}
	}
	if c == 0 || appFont == nil {
		return false
	}
	var buf sfnt.Buffer
	if i, err := appFont.GlyphIndex(&buf, c); err != nil || i == 0 {
		return false
	}
	face, err := opentype.NewFace(appFont, &opentype.FaceOptions{Size: float64(size) * 0.42, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		return false
	}
	defer face.Close()
	bounds, _, ok := face.GlyphBounds(c)
	if !ok {
		return false
	}
	// centred by the glyph's own ink, not its advance and the line's
	// ascent, so every letter sits in the middle
	w := (bounds.Max.X - bounds.Min.X).Ceil()
	hgt := (bounds.Max.Y - bounds.Min.Y).Ceil()
	x := (size-w)/2 - bounds.Min.X.Floor()
	y := (size-hgt)/2 - bounds.Min.Y.Floor()
	d := font.Drawer{Dst: img, Src: image.NewUniform(color.White), Face: face, Dot: fixed.P(x, y)}
	d.DrawString(string(c))
	return true
}

// AppWorkerJS is the app's service worker: it leaves every request alone
// but the app's own window opening, and answers that with a page saying
// to open magpie when magpie can't be reached. lang is magpie's
// language setting; "system" (or none) takes the browser's.
//
// The window is fetched by navigation preload, never by the worker's own
// fetch: that one goes as Sec-Fetch-Dest empty, where magpie serves the
// page alone, not the shell that frames it. A browser with no preload
// gets no offline page, and the network as before.
func AppWorkerJS(lang string) []byte {
	off, _ := json.Marshal(offlinePage(lang))
	var b bytes.Buffer
	b.WriteString(`"use strict";
const OFF = `)
	b.Write(off)
	b.WriteString(`;
const off = () => new Response(OFF, { status: 503, headers: { "Content-Type": "text/html; charset=utf-8", "Cache-Control": "no-store" } });
self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (e) => {
  const np = self.registration.navigationPreload;
  e.waitUntil(Promise.all([np ? np.enable() : null, self.clients.claim()]));
});
self.addEventListener("fetch", (e) => {
  const r = e.request;
  if (r.mode !== "navigate" || r.destination !== "document" || !self.registration.navigationPreload) return;
  e.respondWith(Promise.resolve(e.preloadResponse).then((p) => p || fetch(r), off).catch(off));
});
`)
	return b.Bytes()
}

// offlineWords are the offline page's words in each language magpie
// speaks.
var offlineWords = map[string][2]string{
	"en":    {"magpie isn't running", "Open magpie and this app opens with it."},
	"zh":    {"magpie 没有在运行", "打开 magpie，这个应用就会打开。"},
	"zh-TW": {"magpie 沒有在執行", "打開 magpie，這個應用程式就會打開。"},
	"ja":    {"magpie が起動していません", "magpie を開くと、このアプリも開きます。"},
	"de":    {"magpie läuft nicht", "Öffne magpie, dann öffnet sich diese App."},
}

// offlinePage is the page the app shows while magpie can't be reached:
// it tries again every few seconds, so it opens once magpie does.
func offlinePage(lang string) string {
	words, ok := offlineWords[lang]
	pick := ""
	if !ok {
		// the browser's language, chosen in the page
		words = offlineWords["en"]
		all, _ := json.Marshal(offlineWords)
		pick = `<script>(function(){var w=` + string(all) + `,l=(navigator.language||"en").toLowerCase(),k=/^zh-(tw|hk|mo|hant)/.test(l)?"zh-TW":l.slice(0,2);var x=w[k];if(x){document.documentElement.lang=k;document.querySelector("h1").textContent=x[0];document.querySelector("p").textContent=x[1];}})();</script>`
		lang = "en"
	}
	return `<!doctype html><html lang="` + lang + `"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="refresh" content="3"><title>magpie</title><style>` +
		`html{color-scheme:light dark}body{margin:0;min-height:100vh;display:grid;place-content:center;gap:6px;text-align:center;font:14px/1.45 -apple-system,BlinkMacSystemFont,"Segoe UI",system-ui,sans-serif;color:#1d1d1f;background:#fff}` +
		`h1{margin:0;font-size:17px;font-weight:600}p{margin:0;color:#6e6e73}@media(prefers-color-scheme:dark){body{color:#f5f5f7;background:#1c1c1e}p{color:#98989d}}` +
		`</style></head><body><h1>` + words[0] + `</h1><p>` + words[1] + `</p>` + pick + `</body></html>`
}
