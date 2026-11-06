package gui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// matrix0487 (Discord, 11-05): image-files, set only with `magpie provider
// set <id> image-files=off` (#5407), is offered in the editor of a provider
// on DeepSeek's own API, the built-in one or a custom one there, and only
// there. A value set from the CLI opens as set, a Save that leaves it out
// keeps it, and the editor's tick saves it either way.
func TestProviderSaveImageFiles(t *testing.T) {
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
	info := func(id string) providerJSON {
		t.Helper()
		p, err := provider.Find(id)
		if err != nil {
			t.Fatal(err)
		}
		in := providerInfo(*p, nil)
		if in.ImageFilesOff != p.ImageFilesOff {
			t.Fatalf("%s: the editor is shown off=%v, saved off=%v", id, in.ImageFilesOff, p.ImageFilesOff)
		}
		return in
	}

	// the built-in DeepSeek, set off from the CLI (the field it writes)
	ds, err := provider.FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	ds.Key = "sk-ds"
	ds.ImageFilesOff = true
	if err := provider.Save(ds); err != nil {
		t.Fatal(err)
	}
	if got := info("deepseek"); !got.TakesImageFiles || !got.ImageFilesOff {
		t.Fatalf("built-in DeepSeek set off from the CLI: offered %v, shown off %v", got.TakesImageFiles, got.ImageFilesOff)
	}
	edit := `{"id":"deepseek","from":"deepseek","name":"DeepSeek","preset":"deepseek","key":"","chat":"` + ds.Chat + `","responses":"` + ds.Responses + `","anthropic":"` + ds.Anthropic + `","catalog":"deepseek","models":[],"headers":{}`
	post(edit + `}`)
	if !info("deepseek").ImageFilesOff {
		t.Fatal("a Save that left it out turned the CLI's off back on")
	}
	post(edit + `,"imageFilesOff":false}`)
	if info("deepseek").ImageFilesOff {
		t.Fatal("ticked on in the editor, still saved off")
	}
	post(edit + `,"imageFilesOff":true}`)
	if !info("deepseek").ImageFilesOff {
		t.Fatal("unticked in the editor, still saved on")
	}

	// a custom provider on DeepSeek's own API is offered it, added off
	post(`{"id":"mine","name":"Mine","key":"sk-m","anthropic":"https://api.deepseek.com/anthropic","icon":"generic","new":true,"imageFilesOff":true}`)
	if got := info("mine"); !got.TakesImageFiles || !got.ImageFilesOff {
		t.Fatalf("custom on api.deepseek.com: offered %v, off %v", got.TakesImageFiles, got.ImageFilesOff)
	}

	// another base, a relay serving DeepSeek's models included, is not
	post(`{"id":"relay","name":"Relay","key":"sk-r","chat":"https://relay.example.com/deepseek/v1","icon":"generic","new":true}`)
	post(`{"id":"near","name":"Near","key":"sk-n","chat":"https://api.deepseek.com.example.com/v1","icon":"generic","new":true}`)
	for _, id := range []string{"relay", "near"} {
		if info(id).TakesImageFiles {
			t.Errorf("%s isn't DeepSeek's own API, yet offered image-files", id)
		}
	}
}
