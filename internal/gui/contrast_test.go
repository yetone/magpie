package gui

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// Increase contrast (usrname1833 on Discord) is saved from Settings, kept
// by a save that doesn't send it, refused when it isn't one of its values,
// and drawn by boot.js before the first paint: as written when turned on,
// left to the system's prefers-contrast when never set, and not at all
// when turned off.
func TestContrastSetting(t *testing.T) {
	srv := appearanceHome(t)
	boot := func() string { return lookServe(srv, "GET", "/boot.js", "").Body.String() }
	style := func(b string) string {
		i := strings.Index(b, `e.textContent=`)
		if i < 0 {
			return ""
		}
		var css string
		dec := json.NewDecoder(strings.NewReader(b[i+len(`e.textContent=`):]))
		if err := dec.Decode(&css); err != nil {
			t.Fatalf("boot.js's stylesheet: %v", err)
		}
		return css
	}
	// never set: drawn only while the system asks
	if b := boot(); style(b) != contrastFollowCSS || !strings.Contains(b, `"look":","`) {
		t.Fatalf("unset contrast's boot.js: %s", b)
	}
	if rec := lookServe(srv, "POST", "/api/settings", `{"theme":"light","lang":"en","contrast":"more"}`); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got := settings.Load().Contrast; got != settings.ContrastMore {
		t.Fatalf("saved contrast %q", got)
	}
	if b := boot(); style(b) != contrastCSS || !strings.Contains(b, `"look":",more"`) {
		t.Fatalf("contrast on, boot.js: %s", b)
	}
	var css struct{ CSS, Key string }
	json.Unmarshal(lookServe(srv, "GET", "/api/appearance/css", "").Body.Bytes(), &css)
	if css.CSS != contrastCSS || css.Key != ",more" {
		t.Fatalf("/api/appearance/css with contrast on: %q %q", css.Key, css.CSS)
	}
	// a page that never sent it keeps it
	if rec := lookServe(srv, "POST", "/api/settings", `{"theme":"dark","lang":"en"}`); rec.Code != http.StatusOK || settings.Load().Contrast != settings.ContrastMore {
		t.Fatalf("a save without contrast took it away: %d %q", rec.Code, settings.Load().Contrast)
	}
	for _, bad := range []string{"high", "More", "1", "more;}body{display:none"} {
		b, _ := json.Marshal(map[string]string{"theme": "light", "lang": "en", "contrast": bad})
		if rec := lookServe(srv, "POST", "/api/settings", string(b)); rec.Code == http.StatusOK || settings.Load().Contrast != settings.ContrastMore {
			t.Fatalf("contrast %q was taken: %d", bad, rec.Code)
		}
	}
	// turned off, it is off even while the system asks
	if rec := lookServe(srv, "POST", "/api/settings", `{"theme":"light","lang":"en","contrast":"normal"}`); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if b := boot(); style(b) != "" || !strings.Contains(b, `"look":",normal"`) {
		t.Fatalf("contrast off, boot.js: %s", b)
	}
	// and with an accent, contrast's rules come after the accent's
	if rec := lookServe(srv, "POST", "/api/settings", `{"theme":"light","lang":"en","contrast":"more","accent":"#0d9488"}`); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got := style(boot()); !strings.HasSuffix(got, contrastCSS) || !strings.Contains(got, "--accent:") {
		t.Fatalf("accent and contrast: %s", got)
	}
}

// contrastBlocks are contrast.css's colour rules: the light one, the dark
// one under the system's dark theme and the dark one for a picked dark
// theme, each as its colours by name.
func contrastBlocks(t *testing.T) (light, darkSystem, darkPicked map[string]string) {
	t.Helper()
	css := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(contrastCSS, "")
	body := func(sel string) map[string]string {
		i := strings.Index(css, sel+" {")
		if i < 0 {
			t.Fatalf("no %s in contrast.css", sel)
		}
		s := css[i+len(sel)+2:]
		s = s[:strings.IndexByte(s, '}')]
		out := map[string]string{}
		for _, kv := range strings.Split(s, ";") {
			if k, v, ok := strings.Cut(kv, ":"); ok {
				out[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
		return out
	}
	return body(`html:not(.omarchy):root:not([data-theme="dark"]), html:not(.omarchy):root:not([data-theme="dark"]) body.panel`),
		body(`html:not(.omarchy):root:not([data-theme="light"]), html:not(.omarchy):root:not([data-theme="light"]) body.panel`),
		body(`html:not(.omarchy):root[data-theme="dark"], html:not(.omarchy):root[data-theme="dark"] body.panel`)
}

// The dark rules outweigh the light one only for what they set, so each
// sets every colour the light one does, and the two dark ones are the
// same.
func TestContrastSetsEveryColourInBoth(t *testing.T) {
	light, darkSystem, darkPicked := contrastBlocks(t)
	if len(light) < 10 {
		t.Fatalf("the light rule sets %d colours", len(light))
	}
	for k := range light {
		if _, ok := darkSystem[k]; !ok {
			t.Errorf("the dark theme keeps light's %s", k)
		}
	}
	if len(darkSystem) != len(light) {
		t.Errorf("dark sets %d colours, light %d", len(darkSystem), len(light))
	}
	for k, v := range darkSystem {
		if darkPicked[k] != v {
			t.Errorf("%s: %q under the system's dark theme, %q picked", k, v, darkPicked[k])
		}
	}
	if len(darkPicked) != len(darkSystem) {
		t.Errorf("the picked dark theme sets %d colours, the system's %d", len(darkPicked), len(darkSystem))
	}
}

// Text reads at WCAG AA (4.5:1) and the edges of lists and fields at 3:1,
// on every ground they are drawn on, the tray panel's and a hovered or
// selected row's included, in light and dark, at magpie's own accent.
func TestContrastRatios(t *testing.T) {
	light, dark, _ := contrastBlocks(t)
	raw, err := os.ReadFile("assets/app.css")
	if err != nil {
		t.Fatal(err)
	}
	appCSS := string(raw)
	// the colour sel sets for name in app.css
	app := func(sel, name string) rgb {
		i := strings.Index(appCSS, sel+" {")
		if i < 0 {
			t.Fatalf("no %q in app.css", sel)
		}
		s := appCSS[i:]
		s = s[:strings.IndexByte(s, '}')]
		m := regexp.MustCompile(regexp.QuoteMeta(name) + `:\s*(#[0-9a-f]{6})`).FindStringSubmatch(s)
		if m == nil {
			t.Fatalf("no %s in app.css's %s", name, sel)
		}
		c, _ := parseHex(m[1])
		return c
	}
	hex := func(set map[string]string, name string) rgb {
		c, ok := parseHex(set[name])
		if !ok {
			t.Fatalf("contrast.css's %s is %q", name, set[name])
		}
		return c
	}
	// color-mix(in srgb, var(--accent) N%, #rrggbb)
	sel := func(set map[string]string, accent rgb) rgb {
		m := regexp.MustCompile(`^color-mix\(in srgb, var\(--accent\) (\d+)%, (#[0-9a-f]{6})\)$`).FindStringSubmatch(set["--sel"])
		if m == nil {
			t.Fatalf("contrast.css's --sel is %q", set["--sel"])
		}
		p, _ := strconv.Atoi(m[1])
		base, _ := parseHex(m[2])
		return base.mix(accent, float64(p)/100)
	}
	ratio := func(a, b rgb) float64 {
		la, lb := a.luminance(), b.luminance()
		return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
	}
	for _, th := range []struct {
		name  string
		set   map[string]string
		root  string
		panel string
	}{
		{"light", light, ":root", `:root[data-theme="light"] body.panel`},
		{"dark", dark, `:root[data-theme="dark"]`, `:root[data-theme="dark"] body.panel`},
	} {
		accent := app(th.root, "--accent")
		grounds := map[string]rgb{
			"bg": app(th.root, "--bg"), "card": app(th.root, "--card"), "card-2": app(th.root, "--card-2"), "pill": app(th.root, "--pill"),
			"panel bg": app(th.panel, "--bg"), "panel card": app(th.panel, "--card"), "panel pill": app(th.panel, "--pill"),
			"pill-hover": hex(th.set, "--pill-hover"), "sel": sel(th.set, accent),
		}
		for _, name := range []string{"--fg", "--fg-2", "--muted", "--faint", "--ctl-fg"} {
			c := hex(th.set, name)
			for g, gc := range grounds {
				if r := ratio(c, gc); r < 4.5 {
					t.Errorf("%s: %s on %s reads at %.2f:1", th.name, name, g, r)
				}
			}
		}
		for g, gc := range grounds {
			if r := ratio(accent, gc); r < 4.5 {
				t.Errorf("%s: the accent (links, the focus ring) on %s reads at %.2f:1", th.name, g, r)
			}
		}
		line := hex(th.set, "--line")
		for _, g := range []string{"bg", "card", "card-2", "pill", "panel bg", "panel card", "panel pill"} {
			if r := ratio(line, grounds[g]); r < 3 {
				t.Errorf("%s: --line on %s is %.2f:1", th.name, g, r)
			}
		}
		// a separator is plainer than an edge, but clearer than app.css's
		if r, was := ratio(hex(th.set, "--line-2"), grounds["card"]), ratio(app(th.root, "--line-2"), grounds["card"]); r < 1.5 || r <= was {
			t.Errorf("%s: --line-2 on a card is %.2f:1, app.css's %.2f:1", th.name, r, was)
		}
		// a hovered or selected row stands further off the card than app.css's
		for _, name := range []string{"--pill-hover"} {
			if r, was := ratio(hex(th.set, name), grounds["card"]), ratio(app(th.root, name), grounds["card"]); r <= was {
				t.Errorf("%s: %s off a card is %.2f:1, app.css's %.2f:1", th.name, name, r, was)
			}
		}
		if r, was := ratio(grounds["sel"], grounds["card"]), ratio(app(th.root, "--sel"), grounds["card"]); r <= was {
			t.Errorf("%s: the selection off a card is %.2f:1, app.css's %.2f:1", th.name, r, was)
		}
	}
}
