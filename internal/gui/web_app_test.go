package gui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	_ "image/png"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// magpie web added to an iPhone's home screen opened in Safari, with its
// address bar (ninja_s6114 on Discord): the page named no web app
// manifest and no apple-mobile-web-app-capable. Now it does, and the
// manifest and the icons it and the page name are served, with their
// types, to a browser that asks for them without the page's cookie, as a
// home screen or an install prompt may. Nothing else is.
func TestWebAppAddsToHomeScreen(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	const key = "0123456789abcdef-key"
	srv := webHandler("3430", key, 0, webPageHandler(t, webHost{}, nil))
	do := func(method, path string, signedIn bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		if signedIn {
			r.AddCookie(&http.Cookie{Name: "magpie_web_3430", Value: key})
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		return rec
	}

	page := do("GET", "/", true)
	if page.Code != http.StatusOK {
		t.Fatalf("the page, signed in: %d", page.Code)
	}
	html := page.Body.String()
	for _, want := range []string{
		`<meta name="apple-mobile-web-app-capable" content="yes">`,
		`<meta name="mobile-web-app-capable" content="yes">`,
		`<meta name="apple-mobile-web-app-status-bar-style"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the page lacks %s", want)
		}
	}
	// a theme colour a browser's tab would take too would change how magpie
	// web looks in Safari and Chrome; only the app's window takes it
	for _, m := range regexp.MustCompile(`<meta name="theme-color"[^>]*>`).FindAllString(html, -1) {
		if !strings.Contains(m, `media="(display-mode: standalone)`) {
			t.Errorf("a theme colour outside standalone: %s", m)
		}
	}

	link := func(rel string) string {
		m := regexp.MustCompile(`<link rel="` + rel + `" href="([^"]+)"`).FindStringSubmatch(html)
		if m == nil {
			t.Fatalf("the page names no %s", rel)
		}
		return "/" + m[1]
	}
	// type and size of the png at path, fetched with no cookie
	icon := func(path, sizes string) {
		rec := do("GET", path, false)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
			t.Errorf("%s without the cookie: %d %q", path, rec.Code, rec.Header().Get("Content-Type"))
			return
		}
		c, _, err := image.DecodeConfig(bytes.NewReader(rec.Body.Bytes()))
		if err != nil {
			t.Errorf("%s: %v", path, err)
			return
		}
		if got := fmt.Sprintf("%dx%d", c.Width, c.Height); got != sizes {
			t.Errorf("%s is %s, named %s", path, got, sizes)
		}
	}
	icon(link("apple-touch-icon"), "180x180")

	mf := link("manifest")
	rec := do("GET", mf, false)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("%s without the cookie: %d %q", mf, rec.Code, rec.Header().Get("Content-Type"))
	}
	var m struct {
		StartURL string `json:"start_url"`
		Icons    []struct{ Src, Sizes, Type string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	json.Unmarshal(rec.Body.Bytes(), &raw)
	if raw["name"] != "magpie" || raw["display"] != "standalone" || m.StartURL == "" {
		t.Errorf("manifest: name %v, display %v, start_url %q", raw["name"], raw["display"], m.StartURL)
	}
	big := false
	for _, ic := range m.Icons {
		icon("/"+ic.Src, ic.Sizes)
		big = big || ic.Sizes == "512x512"
	}
	if !big {
		t.Error("the manifest has no 512px icon, which Chrome on Android wants to install the page")
	}
	if rec := do("HEAD", mf, false); rec.Code != http.StatusOK {
		t.Errorf("HEAD %s without the cookie: %d", mf, rec.Code)
	}

	// every other file and route still wants the key, and so do these
	// files asked for any other way
	for _, c := range []struct{ method, path string }{
		{"GET", "/"},
		{"GET", "/index.html"},
		{"GET", "/app.js"},
		{"GET", "/boot.js"},
		{"GET", "/icons/favicon.svg"},
		{"GET", "/icons/magpie.svg"},
		{"GET", "/api/state"},
		{"GET", "/api/providers"},
		{"GET", "/manifest.json/"},
		{"GET", "/Manifest.json"},
		{"GET", "/icons/../app.js"},
		{"GET", "/icons/apple-touch-icon.png/../../app.js"},
		{"POST", "/manifest.json"},
		{"PUT", "/icons/icon-512.png"},
	} {
		if rec := do(c.method, c.path, false); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without the cookie: %d, want 401", c.method, c.path, rec.Code)
		}
	}
}
