package gui

import (
	"io/fs"
	"mime"
	"net/http"
	"net/http/httptest"
	"path"
	"testing"
)

// The page's own files go out as assetTypes names them, whatever the
// system's table says (Windows' registry, which another program may have
// changed): their type is set before http.ServeContent would ask the mime
// package, which reads the system's whole table into the heap for good.
func TestAssetTypesHoldAgainstTheRegistry(t *testing.T) {
	for ext := range assetTypes {
		mime.AddExtensionType(ext, "application/x-other")
	}
	t.Cleanup(func() {
		for ext, typ := range assetTypes {
			mime.AddExtensionType(ext, typ)
		}
	})
	h := Handler(nil, nil)
	seen := map[string]bool{}
	fs.WalkDir(staticFS(), ".", func(name string, d fs.DirEntry, err error) error {
		ext := path.Ext(name)
		// index.html is served as / (below); FileServer sends its own name there
		if err != nil || d.IsDir() || seen[ext] || assetTypes[ext] == "" || name == "index.html" {
			return err
		}
		seen[ext] = true
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+name, nil))
		if got := rec.Header().Get("Content-Type"); rec.Code == http.StatusOK && got != assetTypes[ext] {
			t.Errorf("%s served as %q, not %q", name, got, assetTypes[ext])
		} else if rec.Code != http.StatusOK {
			t.Errorf("%s: %d", name, rec.Code)
		}
		return nil
	})
	for _, ext := range []string{".js", ".css", ".svg", ".png", ".jpg", ".json", ".webm"} {
		if !seen[ext] {
			t.Errorf("no %s file served", ext)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("/ served as %q", got)
	}
}
