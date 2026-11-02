package gui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The editor doesn't send that magpie filled an AI Studio key's Gemini URL
// in (GeminiFilled), so its save keeps it: the URL cleared in the form
// stays cleared, and isn't filled in again.
func TestSaveKeepsGeminiCleared(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "google", Name: "Google Gemini", Preset: "google", Chat: provider.AIStudioChat, Key: "AIza-k"}); err != nil {
		t.Fatal(err)
	}
	if p, _ := provider.Find("google"); p == nil || p.Gemini != provider.AIStudioGemini {
		t.Fatalf("not filled in: %+v", p)
	}
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(
		`{"id":"google","name":"Google Gemini","preset":"google","chat":"`+provider.AIStudioChat+`","gemini":""}`)))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	got, err := provider.Find("google")
	if err != nil {
		t.Fatal(err)
	}
	if got.Gemini != "" || got.Key != "AIza-k" {
		t.Fatalf("after clearing it in the editor: gemini %q key %q", got.Gemini, got.Key)
	}
}
