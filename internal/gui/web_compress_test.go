package gui

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// `magpie web` sent its page's ~4 MB of scripts and styles, and the API's
// JSON, as they are: over a 3 Mbps link to a server every load waited for
// all of it (akic404 on Discord). A browser that takes gzip gets them
// gzipped, a client that doesn't gets the bytes as they are, pictures
// already compressed are left alone, and a file asked for again by its
// ETag is still a 304.
func TestWebCompresses(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	srv := webHandler("3430", "0123456789abcdef-key", 0, Handler(webHost{}, nil))
	cookie := &http.Cookie{Name: "magpie_web_3430", Value: "0123456789abcdef-key"}
	get := func(path, enc, etag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(cookie)
		if enc != "" {
			r.Header.Set("Accept-Encoding", enc)
		}
		if etag != "" {
			r.Header.Set("If-None-Match", etag)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		return rec
	}
	// what Chrome and Safari send over plain http
	const browser = "gzip, deflate"
	for _, f := range []string{"i18n.js", "app.js", "routing.js", "app.css", "index.html"} {
		want, err := fs.ReadFile(staticFS(), f)
		if err != nil {
			t.Fatal(err)
		}
		path := "/" + f
		if f == "index.html" {
			path, want = "/", versionedPage(want)
		}
		rec := get(path, browser, "")
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" {
			t.Errorf("%s to a browser that takes gzip: %d, Content-Encoding %q", path, rec.Code, rec.Header().Get("Content-Encoding"))
			continue
		}
		if rec.Body.Len()*2 > len(want) {
			t.Errorf("%s: %d bytes gzipped of %d", path, rec.Body.Len(), len(want))
		}
		zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		got, err := io.ReadAll(zr)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s unzipped isn't the file (%d bytes of %d): %v", path, len(got), len(want), err)
		}
		if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
			t.Errorf("%s: Vary %q, a cache would hand gzip to a client that can't read it", path, rec.Header().Get("Vary"))
		}
		etag := rec.Header().Get("ETag")
		if !strings.HasSuffix(etag, `-gzip"`) {
			t.Errorf("%s gzipped keeps the ETag of other bytes: %q", path, etag)
		}
		if again := get(path, browser, etag); again.Code != http.StatusNotModified {
			t.Errorf("%s asked again with its gzipped ETag: %d, want 304", path, again.Code)
		}
		plain := get(path, "", "")
		if plain.Header().Get("Content-Encoding") != "" || !bytes.Equal(plain.Body.Bytes(), want) {
			t.Errorf("%s to a client that takes no gzip: Content-Encoding %q, %d bytes of %d", path, plain.Header().Get("Content-Encoding"), plain.Body.Len(), len(want))
		}
	}
	if rec := get("/api/state", browser, ""); rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" {
		t.Errorf("/api/state: %d, Content-Encoding %q, want its JSON gzipped", rec.Code, rec.Header().Get("Content-Encoding"))
	}
	if rec := get("/icons/crush.png", browser, ""); rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "" {
		t.Errorf("a PNG: %d, Content-Encoding %q, want it as it is", rec.Code, rec.Header().Get("Content-Encoding"))
	}
	if rec := get("/icons/openai.svg", browser, ""); rec.Header().Get("Content-Encoding") != "gzip" {
		t.Errorf("an SVG is text: Content-Encoding %q, want gzip", rec.Header().Get("Content-Encoding"))
	}
	// and still behind the key
	r := httptest.NewRequest("GET", "/api/state", nil)
	r.Header.Set("Accept-Encoding", browser)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no key: %d", rec.Code)
	}
}

// A script or stylesheet asked for by the address the page names it by,
// its content's hash, is kept by the browser for good and not asked for
// again at each load; the same file under another version's hash, or
// none, is still asked for again (TestPageFilesRevalidate).
func TestVersionedFilesKept(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	srv := Handler(webHost{}, nil)
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}
	page := get("/").Body.String()
	for _, f := range []string{"app.js", "routing.js", "i18n.js", "app.css"} {
		b, err := fs.ReadFile(staticFS(), f)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		named := "/" + f + "?v=" + hex.EncodeToString(sum[:6])
		if !strings.Contains(page, `"`+named[1:]+`"`) {
			t.Fatalf("the page doesn't name %s", named)
		}
		if cc := get(named).Header().Get("Cache-Control"); cc != "max-age=31536000, immutable" {
			t.Errorf("%s: Cache-Control %q, want it kept for good", named, cc)
		}
		if cc := get("/" + f + "?v=000000000000").Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s under another version's hash: Cache-Control %q, want no-cache", f, cc)
		}
	}
	if cc := get("/").Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("the page: Cache-Control %q, want no-cache", cc)
	}
}
