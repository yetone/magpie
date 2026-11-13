package gui

import (
	"encoding/base64"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
	"github.com/yetone/magpie/internal/usageapi"
)

// The usage:day read (plugin.PageReads): a day of usage for a plugin's
// page, by agent and by model, with the logos the Usage page draws for
// them. A page loads nothing but its own files and data: pictures
// (pagePolicy), so each logo comes as a data: URL, once for each name.

// pageIcon is a logo as a page draws it: Mono a one-colour mark the page
// fills with its text colour, as the window's mask icons are (app.js's
// icon), else a picture drawn as it is.
type pageIcon struct {
	Src  string `json:"src"`
	Mono bool   `json:"mono,omitempty"`
}

// maxPageIcon is the largest logo a read carries: a picture of the user's
// own past it is drawn as the generic one.
const maxPageIcon = 256 << 10

// pageIconOf is the logo named name, as app.js's icon finds it: a picture
// the user gave a provider (file:…), else the bundled SVG or PNG; ok is
// false for "generic", "" and a name with no file.
func pageIconOf(name string) (pageIcon, bool) {
	if name == "" || name == "generic" {
		return pageIcon{}, false
	}
	if own, ok := strings.CutPrefix(name, "file:"); ok {
		f, _ := provider.IconServed(own)
		if f == "" {
			return pageIcon{}, false
		}
		b, err := os.ReadFile(f)
		if err != nil || len(b) > maxPageIcon {
			return pageIcon{}, false
		}
		return pageIcon{Src: dataURL(own, b)}, true
	}
	// a URL or a path is no bundled name (app.js draws it as generic)
	if strings.ContainsAny(name, ":/\\") {
		return pageIcon{}, false
	}
	for _, ext := range []string{".svg", ".png"} {
		b, err := fs.ReadFile(assets, "assets/icons/"+name+ext)
		if err != nil || len(b) > maxPageIcon {
			continue
		}
		return pageIcon{Src: dataURL(name+ext, b), Mono: ext == ".svg" && !strings.HasSuffix(name, "-color")}, true
	}
	return pageIcon{}, false
}

func dataURL(name string, b []byte) string {
	ctype := "image/svg+xml"
	switch strings.ToLower(path.Ext(name)) {
	case ".png":
		ctype = "image/png"
	case ".jpg", ".jpeg":
		ctype = "image/jpeg"
	case ".webp":
		ctype = "image/webp"
	}
	return "data:" + ctype + ";base64," + base64.StdEncoding.EncodeToString(b)
}

// pageDay is the usage:day read: params {date: "YYYY-MM-DD", from:
// "YYYY-MM-DD" or "all", tz: an IANA zone}, all optional (today, the day
// alone, this computer's zone): with a from it is the days from there to
// date (usageapi.SpanOf), a week's or a month's or all time's.
func pageDay(params map[string]any) (any, error) {
	date, _ := params["date"].(string)
	from, _ := params["from"].(string)
	tz, _ := params["tz"].(string)
	d, err := usageapi.SpanOf(from, date, usage.Zone(tz))
	if err != nil {
		return nil, err
	}
	icons := map[string]pageIcon{}
	want := func(name string) {
		if _, ok := icons[name]; ok {
			return
		}
		if ic, ok := pageIconOf(name); ok {
			icons[name] = ic
		}
	}
	for _, a := range d.Agents {
		want(a.Icon)
		for _, m := range a.Models {
			want(m.Icon)
		}
	}
	// a cost as Settings shows a sum of several providers': in USD, or in
	// CNY at the rate known now
	cur := settings.Load().TotalCostIn()
	return struct {
		usageapi.Day
		Icons    map[string]pageIcon `json:"icons"`
		Currency string              `json:"currency"`
		Rate     float64             `json:"rate"` // CNY per USD, 0 when unknown
	}{d, icons, cur, currentFX().Rate}, nil
}
