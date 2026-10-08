package gui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// The page's files go out to be asked for again by their content's hash,
// so a browser or a cache in front of `magpie web` never keeps an older
// version's app.js under a newer page (Jorben on Discord); an unchanged
// one comes back as a 304, and boot.js and the API keep their own rules.
func TestPageFilesRevalidate(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	srv := Handler(webHost{}, nil)
	get := func(path, etag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if etag != "" {
			r.Header.Set("If-None-Match", etag)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		return rec
	}
	tags := map[string]string{}
	for _, p := range []string{"/", "/app.js", "/app.css", "/i18n.js", "/icons/openai.svg"} {
		rec := get(p, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", p, rec.Code)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s: Cache-Control %q, want no-cache", p, cc)
		}
		etag := rec.Header().Get("ETag")
		if etag == "" {
			t.Errorf("%s: no ETag to ask for it again by", p)
			continue
		}
		tags[p] = etag
		if again := get(p, etag); again.Code != http.StatusNotModified {
			t.Errorf("%s asked again with its ETag: %d, want 304", p, again.Code)
		}
		if again := get(p, `"another-version"`); again.Code != http.StatusOK || again.Body.Len() == 0 {
			t.Errorf("%s asked with an older ETag: %d, %d bytes, want the file", p, again.Code, again.Body.Len())
		}
	}
	if tags["/app.js"] != "" && tags["/app.js"] == tags["/app.css"] {
		t.Errorf("app.js and app.css share the ETag %s: it isn't their content's", tags["/app.js"])
	}
	if cc := get("/boot.js", "").Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("boot.js: Cache-Control %q, want no-store", cc)
	}
	if rec := get("/wails/runtime.js", ""); rec.Code != http.StatusNotFound || rec.Header().Get("ETag") != "" {
		t.Errorf("magpie web has no Wails runtime: %d, ETag %q", rec.Code, rec.Header().Get("ETag"))
	}
}

// A cache in front of `magpie web` that kept a page file from before they
// were sent to be asked for again goes on serving it whatever magpie says
// now: a Docker user behind an HTTPS proxy had v0.1.630's page run
// v0.1.582's app.js and routing.js, in a private window too. app.js
// stopped at its first lines over an element the page no longer had
// (#moreSubs), routing.js then on app.js's pinOf, and the page was blank
// under its tabs. The page names each of its own scripts and styles by its
// content's hash, so a new version's page asks for new addresses, which no
// cache holds; boot.js, which the API writes each time, keeps its own.
func TestPageNamesItsFilesByContent(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	srv := Handler(webHost{}, nil)
	get := func(path, etag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if etag != "" {
			r.Header.Set("If-None-Match", etag)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		return rec
	}
	page := get("/", "")
	if page.Code != http.StatusOK {
		t.Fatalf("/: %d", page.Code)
	}
	if ct := page.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("/: Content-Type %q", ct)
	}
	body := page.Body.String()
	for _, f := range []string{"app.js", "routing.js", "library.js", "plugins.js", "sessions.js", "context.js", "i18n.js", "compat.js", "app.css", "routing.css", "context.css"} {
		b, err := fs.ReadFile(staticFS(), f)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		want := f + "?v=" + hex.EncodeToString(sum[:6])
		if !strings.Contains(body, `"`+want+`"`) {
			t.Errorf("the page doesn't name %s by its content (%s)", f, want)
			continue
		}
		if rec := get("/"+want, ""); rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), b) {
			t.Errorf("/%s: %d, %d bytes, want the file", want, rec.Code, rec.Body.Len())
		}
	}
	if !strings.Contains(body, `src="boot.js"`) {
		t.Error("boot.js is the API's, asked for afresh each time: it keeps its name")
	}
	if again := get("/", page.Header().Get("ETag")); again.Code != http.StatusNotModified {
		t.Errorf("/ asked again with its ETag: %d, want 304", again.Code)
	}
}
