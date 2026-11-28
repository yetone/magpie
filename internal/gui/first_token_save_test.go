package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The editor's save sets a provider's own first-token time (GoForceX on
// X), keeps it when a save leaves it out (an older form), clears it with
// 0 or null, refuses one past FirstTokenLongest, and /api/providers tells
// it for the editor to show.
func TestProviderFirstTokenSave(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "ft-test", Name: "FT", Chat: "https://example.invalid/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	save := func(more string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(`{"id":"ft-test","name":"FT","chat":"https://example.invalid/v1"`+more+`}`)))
		return w
	}
	saved := func() int {
		got, err := provider.Find("ft-test")
		if err != nil {
			t.Fatal(err)
		}
		return got.FirstToken
	}
	if w := save(`,"firstToken":20`); w.Code != 200 || saved() != 20 {
		t.Fatalf("set: %d %s, saved %d", w.Code, w.Body, saved())
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/providers", nil))
	var state providersJSON
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	shown := -1
	for _, q := range state.Providers {
		if q.ID == "ft-test" {
			shown = q.FirstToken
		}
	}
	if shown != 20 {
		t.Fatalf("shown %d", shown)
	}
	if w := save(""); w.Code != 200 || saved() != 20 {
		t.Fatalf("left out: %d %s, saved %d", w.Code, w.Body, saved())
	}
	for _, bad := range []string{`,"firstToken":-1`, `,"firstToken":601`, `,"firstToken":"x"`} {
		if w := save(bad); w.Code == 200 || saved() != 20 {
			t.Fatalf("%s taken, saved %d", bad, saved())
		}
	}
	if w := save(`,"firstToken":null`); w.Code != 200 || saved() != 0 {
		t.Fatalf("null: %d %s, saved %d", w.Code, w.Body, saved())
	}
	save(`,"firstToken":20`)
	if w := save(`,"firstToken":0`); w.Code != 200 || saved() != 0 {
		t.Fatalf("0: %d %s, saved %d", w.Code, w.Body, saved())
	}
}
