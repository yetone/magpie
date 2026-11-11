//go:build packedassets

package gui

import (
	"bytes"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"testing"
)

// The packed UI must be the tree's: every file, byte for byte, and no other.
// Run after `go run ./build/assetpack internal/gui/assets internal/gui/assets.zip`.
func TestPackedAssetsAreTheTree(t *testing.T) {
	tree := os.DirFS("assets")
	var want []string
	fs.WalkDir(tree, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			want = append(want, p)
		}
		return nil
	})
	if len(want) < 100 {
		t.Fatalf("only %d files in the tree", len(want))
	}
	var got []string
	fs.WalkDir(assets, "assets", func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			got = append(got, p[len("assets/"):])
		}
		return nil
	})
	sort.Strings(want)
	sort.Strings(got)
	if len(got) != len(want) {
		t.Fatalf("packed has %d files, the tree %d", len(got), len(want))
	}
	for i, n := range want {
		if got[i] != n {
			t.Fatalf("file %d: packed %q, tree %q", i, got[i], n)
		}
		a, _ := fs.ReadFile(tree, n)
		b, err := assets.ReadFile("assets/" + n)
		if err != nil || !bytes.Equal(a, b) {
			t.Fatalf("%s differs from the tree (err %v)", n, err)
		}
	}
}

// The page is served as the tree's was: index at /, a range of a video, 404.
func TestPackedAssetsServe(t *testing.T) {
	h := http.FileServer(http.FS(staticFS()))
	get := func(path, rng string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if rng != "" {
			r.Header.Set("Range", rng)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	idx, _ := os.ReadFile("assets/index.html")
	if w := get("/", ""); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), idx) {
		t.Fatalf("/ gave %d, %d bytes (index.html is %d)", w.Code, w.Body.Len(), len(idx))
	}
	webm, _ := os.ReadFile("assets/plugins/redact/xq_light.webm")
	w := get("/plugins/redact/xq_light.webm", "bytes=100-199")
	if w.Code != 206 || !bytes.Equal(w.Body.Bytes(), webm[100:200]) {
		t.Fatalf("range of the video gave %d, %d bytes", w.Code, w.Body.Len())
	}
	if w := get("/nope.js", ""); w.Code != 404 {
		t.Fatalf("a missing file gave %d", w.Code)
	}
	if lm := get("/app.js", "").Header().Get("Last-Modified"); lm != "" {
		t.Fatalf("Last-Modified %q: an embedded file has none", lm)
	}
}
