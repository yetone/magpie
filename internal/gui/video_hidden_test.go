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

// axelburks on Discord: a provider's editor has its video models apart
// (filmIds), as its image models are; those unticked there are saved as
// HiddenVideomakers, kept by a Save that leaves them out and cleared by [],
// and a save of one leaves the other's alone.
func TestVideoModelsHidden(t *testing.T) {
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
	const base = `"id":"films","name":"Films","key":"sk-f","chat":"https://films.example/v1","icon":"generic"`
	post(`{` + base + `,"new":true}`)
	if err := catalog.SaveLive("films", "https://films.example/v1", []catalog.Model{{ID: "text"}, {ID: "img-a", Draws: true}, {ID: "vid-a", Films: true}, {ID: "vid-b", Films: true}}); err != nil {
		t.Fatal(err)
	}
	info := func() providerJSON {
		t.Helper()
		p, err := provider.Find("films")
		if err != nil {
			t.Fatal(err)
		}
		out := providerInfo(*p, nil)
		if !slices.Equal(out.HiddenVideomakers, p.HiddenVideomakers) {
			t.Fatalf("the editor is shown %v, saved %v", out.HiddenVideomakers, p.HiddenVideomakers)
		}
		return out
	}
	if got := info(); !slices.Equal(got.FilmIDs, []string{"vid-a", "vid-b"}) || !slices.Equal(got.DrawIDs, []string{"img-a"}) {
		t.Fatalf("editor's video models %v, image models %v", got.FilmIDs, got.DrawIDs)
	}

	post(`{` + base + `,"from":"films","models":[],"headers":{},"hiddenVideomakers":["vid-a"],"hiddenDrawers":["img-a"]}`)
	if got := info(); !slices.Equal(got.HiddenVideomakers, []string{"vid-a"}) || !slices.Equal(got.HiddenDrawers, []string{"img-a"}) {
		t.Fatalf("saved videos %v, images %v", got.HiddenVideomakers, got.HiddenDrawers)
	}
	post(`{` + base + `,"from":"films","models":[],"headers":{}}`)
	if got := info(); !slices.Equal(got.HiddenVideomakers, []string{"vid-a"}) {
		t.Fatalf("a Save that left them out changed them: %v", got.HiddenVideomakers)
	}
	post(`{` + base + `,"from":"films","models":[],"headers":{},"hiddenVideomakers":[]}`)
	if got := info(); len(got.HiddenVideomakers) != 0 || !slices.Equal(got.HiddenDrawers, []string{"img-a"}) {
		t.Fatalf("[] kept videos %v, or touched images %v", got.HiddenVideomakers, got.HiddenDrawers)
	}
}
