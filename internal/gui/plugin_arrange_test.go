package gui

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// The sidebar's plugin pages are listed in the order they were dragged to
// (0lVeer on X), and a save of the Settings page leaves that order be.
func TestPluginPagesArrangeKept(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("APPDATA", filepath.Join(h, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(h, "AppData", "Local"))
	post := func(path, body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	post("/api/plugin-pages/arrange", `{"order":["plugin-b"," plugin-b","plugin-a"]}`)
	want := []string{"plugin-b", "plugin-a"}
	if got := settings.Load().PluginPageOrder; !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	post("/api/settings", `{"theme":"dark"}`)
	if got := settings.Load().PluginPageOrder; !slices.Equal(got, want) {
		t.Fatalf("a Settings save left %v, want %v", got, want)
	}
}
