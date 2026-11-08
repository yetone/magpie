package gui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// A provider's Save carries what its editor's Model settings changed
// (modelPrefs), and makes it with the rest (ARNO on Discord: each level
// ticked was applied on its own, before the Save).
func TestProviderSaveModelPrefs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(body)))
		return w
	}
	if w := post(`{"id":"relay","name":"Relay","key":"k","chat":"http://127.0.0.1:1/v1","models":["sol"],"new":true}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := post(`{"id":"relay","from":"relay","name":"Relay","chat":"http://127.0.0.1:1/v1","models":["sol"],
		"modelPrefs":{"sol":{"name":"My Sol","efforts":["low","high"],"images":true}}}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	s := settings.Load()
	if s.ModelNames["relay/sol"] != "My Sol" || !slices.Equal(s.ModelEfforts["relay/sol"], []string{"low", "high"}) || !s.ModelImages["relay/sol"] {
		t.Fatalf("names %v, efforts %v, images %v", s.ModelNames, s.ModelEfforts, s.ModelImages)
	}
	// the model it is the same as, for the groups magpie finds (#583)
	if w := post(`{"id":"relay","from":"relay","name":"Relay","chat":"http://127.0.0.1:1/v1","models":["sol"],"modelPrefs":{"sol":{"same":"deepseek-v4.1-flash"}}}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got := settings.Load().ModelSameAs["relay/sol"]; got != "deepseek-v4.1-flash" {
		t.Fatalf("same as %q", got)
	}
	// one it refuses fails the Save, saying why
	if w := post(`{"id":"relay","from":"relay","name":"Relay","chat":"http://127.0.0.1:1/v1","models":["sol"],"modelPrefs":{"sol":{"efforts":["loud"]}}}`); w.Code == 200 || !strings.Contains(w.Body.String(), "loud") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	// the model's own headers (axelburks on Discord), shown back on the
	// model; a save that leaves them out keeps them, an empty set clears
	if w := post(`{"id":"relay","from":"relay","name":"Relay","chat":"http://127.0.0.1:1/v1","models":["sol"],"modelPrefs":{"sol":{"headers":{" X-Endpoint ":" ep-1 "}}}}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got := settings.Load().ModelHeaders["relay/sol"]; len(got) != 1 || got["X-Endpoint"] != "ep-1" {
		t.Fatalf("headers %v", got)
	}
	if w := post(`{"id":"relay","from":"relay","name":"Relay","chat":"http://127.0.0.1:1/v1","models":["sol"],"modelPrefs":{"sol":{"name":"Sol"}}}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"headers":{"X-Endpoint":"ep-1"}`) {
		t.Fatalf("kept and shown: %d %s", w.Code, w.Body)
	}
	if w := post(`{"id":"relay","from":"relay","name":"Relay","chat":"http://127.0.0.1:1/v1","models":["sol"],"modelPrefs":{"sol":{"headers":{"Bad Name":"x"}}}}`); w.Code == 200 || !strings.Contains(w.Body.String(), "Bad Name") {
		t.Fatalf("a bad name: %d %s", w.Code, w.Body)
	}
	if w := post(`{"id":"relay","from":"relay","name":"Relay","chat":"http://127.0.0.1:1/v1","models":["sol"],"modelPrefs":{"sol":{"headers":{}}}}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got := settings.Load().ModelHeaders["relay/sol"]; got != nil {
		t.Fatalf("cleared: %v", got)
	}
}
