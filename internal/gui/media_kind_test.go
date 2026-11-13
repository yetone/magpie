package gui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// An image model's chip in the editor can be said to make videos (iceloon
// on X: a Grok channel's video model sat among its Image models with no
// way to move it). Saved as modelPrefs' kind, it moves to the video
// models, and the editor is told what was said of it (mediaKinds), so its
// chip shows the tag and the menu its pick.
func TestImageModelMarkedVideo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(body string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
	const base = `"id":"chan","name":"Chan","key":"sk-c","chat":"https://chan.example/v1","icon":"generic"`
	post(`{` + base + `,"new":true}`)
	if err := catalog.SaveLive("chan", "https://chan.example/v1", []catalog.Model{{ID: "grok-4.3"}, {ID: "img-a", Draws: true}, {ID: "clip-x", Draws: true}}); err != nil {
		t.Fatal(err)
	}
	info := func() providerJSON {
		t.Helper()
		p, err := provider.Find("chan")
		if err != nil {
			t.Fatal(err)
		}
		return providerInfo(*p, nil)
	}
	if got := info(); !slices.Equal(got.DrawIDs, []string{"img-a", "clip-x"}) || len(got.FilmIDs) != 0 || got.MediaKinds != nil {
		t.Fatalf("before: images %v videos %v kinds %v", got.DrawIDs, got.FilmIDs, got.MediaKinds)
	}
	post(`{` + base + `,"from":"chan","models":[],"headers":{},"modelPrefs":{"clip-x":{"kind":"video"}}}`)
	got := info()
	if !slices.Equal(got.DrawIDs, []string{"img-a"}) || !slices.Equal(got.FilmIDs, []string{"clip-x"}) || got.MediaKinds["clip-x"] != "video" || len(got.MediaKinds) != 1 {
		t.Fatalf("after: images %v videos %v kinds %v", got.DrawIDs, got.FilmIDs, got.MediaKinds)
	}
	post(`{` + base + `,"from":"chan","models":[],"headers":{},"modelPrefs":{"clip-x":{"kind":""}}}`)
	if got := info(); !slices.Equal(got.DrawIDs, []string{"img-a", "clip-x"}) || len(got.FilmIDs) != 0 || got.MediaKinds != nil {
		t.Fatalf("back to Auto: images %v videos %v kinds %v", got.DrawIDs, got.FilmIDs, got.MediaKinds)
	}
}
