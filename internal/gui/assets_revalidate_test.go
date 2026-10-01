package gui

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
