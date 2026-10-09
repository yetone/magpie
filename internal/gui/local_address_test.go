package gui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A local server's preset added at another address sends its URLs moved
// there (the editor's Address): it is stored as the preset's provider, as
// one added at the default is, apart from its URLs.
func TestProviderSaveLocalPresetAtItsURLs(t *testing.T) {
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
			t.Fatalf("%d", w.Code)
		}
	}
	for _, c := range []struct{ id, name, chat, responses, anthropic string }{
		{"ollama", "Ollama", "http://127.0.0.1:1/v1", "", "http://127.0.0.1:1"},
		{"omlx", "oMLX", "http://127.0.0.1:2/v1", "http://127.0.0.1:2/v1", "http://127.0.0.1:2"},
		{"lmstudio", "LM Studio", "http://127.0.0.1:3/v1", "", ""},
	} {
		// at the default, as the editor sends it with no address: no URLs
		post(`{"id":"` + c.id + `","name":"` + c.name + `","preset":"` + c.id + `","key":"","new":true}`)
		def, err := provider.Find(c.id)
		if err != nil {
			t.Fatal(err)
		}
		post(`{"id":"` + c.id + `","name":"` + c.name + `","preset":"` + c.id + `","key":"","chat":"` + c.chat + `","responses":"` + c.responses + `","anthropic":"` + c.anthropic + `","new":true}`)
		moved, err := provider.Find(c.id + "-2")
		if err != nil {
			t.Fatal(err)
		}
		if moved.Chat != c.chat || moved.Responses != c.responses || moved.Anthropic != c.anthropic {
			t.Fatalf("%s at %q %q %q", c.id, moved.Chat, moved.Responses, moved.Anthropic)
		}
		if !moved.Ready() || moved.Key != "" {
			t.Fatalf("%s not usable with no key: ready %v", c.id, moved.Ready())
		}
		// the same provider as the default's, but for its URLs, and its id
		// and name as the second of the preset
		d, m := *def, *moved
		if m.Name != d.Name+" 2" {
			t.Fatalf("%s named %q", c.id, m.Name)
		}
		d.ID, d.Chat, d.Responses, d.Anthropic = "", "", "", ""
		m.ID, m.Chat, m.Responses, m.Anthropic = "", "", "", ""
		d.Name, m.Name = "", ""
		d.Models, m.Models = nil, nil
		if d.Preset != c.id || d.Icon != c.id || !reflect.DeepEqual(d, m) {
			t.Fatalf("%s moved lost its preset's: default %+v\nmoved %+v", c.id, d, m)
		}
	}
}
