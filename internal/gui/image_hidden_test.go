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
	"github.com/yetone/magpie/internal/settings"
)

// 321sssrt on X: image models unticked in a provider's editor are saved
// as HiddenDrawers, kept by a Save that leaves them out and cleared by [];
// Settings › Models › Images leaves them out, except the one picked there, which
// stays listed as picked with the row saying it is hidden.
func TestImageModelsHidden(t *testing.T) {
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
	const base = `"id":"pics","name":"Pics","key":"sk-p","chat":"https://pics.example/v1","icon":"generic"`
	post(`{` + base + `,"new":true}`)
	if err := catalog.SaveLive("pics", "https://pics.example/v1", []catalog.Model{{ID: "text"}, {ID: "img-a", Draws: true}, {ID: "img-b", Draws: true}}); err != nil {
		t.Fatal(err)
	}
	hidden := func() []string {
		t.Helper()
		p, err := provider.Find("pics")
		if err != nil {
			t.Fatal(err)
		}
		info := providerInfo(*p, nil)
		if !slices.Equal(info.HiddenDrawers, p.HiddenDrawers) {
			t.Fatalf("the editor is shown %v, saved %v", info.HiddenDrawers, p.HiddenDrawers)
		}
		return p.HiddenDrawers
	}
	listed := func() (ids []string) {
		for _, m := range settingsState().ImageGenModels {
			if m.Provider == "pics" {
				ids = append(ids, m.ID)
			}
		}
		return
	}

	post(`{` + base + `,"from":"pics","models":[],"headers":{},"hiddenDrawers":["img-a"]}`)
	if got := hidden(); !slices.Equal(got, []string{"img-a"}) {
		t.Fatalf("saved %v", got)
	}
	if got := listed(); !slices.Equal(got, []string{"pics/img-b"}) {
		t.Fatalf("Settings › Models › Images lists %v", got)
	}
	if settingsState().ImageGenHidden {
		t.Fatal("hidden note with nothing hidden picked")
	}

	st := settings.Load()
	st.ImageGen = "pics/img-a"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	if got := listed(); !slices.Equal(got, []string{"pics/img-a", "pics/img-b"}) {
		t.Fatalf("a hidden pick isn't listed as picked: %v", got)
	}
	if !settingsState().ImageGenHidden {
		t.Fatal("a hidden pick isn't said to be hidden")
	}

	post(`{` + base + `,"from":"pics","models":[],"headers":{}}`)
	if got := hidden(); !slices.Equal(got, []string{"img-a"}) {
		t.Fatalf("a Save that left them out changed them: %v", got)
	}
	post(`{` + base + `,"from":"pics","models":[],"headers":{},"hiddenDrawers":[]}`)
	if got := hidden(); len(got) != 0 {
		t.Fatalf("[] kept %v", got)
	}
	if settingsState().ImageGenHidden {
		t.Fatal("ticked again, still said to be hidden")
	}
}
