package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The provider editor has no MaxImages field: a save that doesn't send it
// must keep what `magpie provider set <id> max-images=8` set, as it keeps
// every other field the CLI alone sets.
func TestProviderSaveKeepsMaxImages(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "eightmax", Name: "Eight Max", Chat: "https://example.invalid/v1", Key: "a", MaxImages: 8}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(body)))
		return w
	}
	set := func() int {
		got, err := provider.Find("eightmax")
		if err != nil {
			t.Fatal(err)
		}
		return got.MaxImages
	}
	if w := post(`{"id":"eightmax","name":"Eight Max","chat":"https://example.invalid/v1"}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if n := set(); n != 8 {
		t.Fatalf("a save leaving it out lost it: %d", n)
	}
	// the state the editor is drawn from says it too
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/providers", nil))
	var state providersJSON
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	shown := false
	for _, q := range state.Providers {
		if q.ID == "eightmax" {
			shown = q.MaxImages == 8
		}
	}
	if !shown {
		t.Fatalf("the providers' state doesn't tell it: %s", w.Body)
	}
}
