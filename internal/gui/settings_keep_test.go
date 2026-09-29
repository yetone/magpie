package gui

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// What the models' page keeps in settings — names, levels, whether a model
// takes images — outlives a save of the Settings page, which never sends it.
func TestSettingsSaveKeepsModelChoices(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	if err := settings.Save(settings.Settings{
		ModelNames:   map[string]string{"p/m": "Mine"},
		ModelEfforts: map[string][]string{"p/m": {"low"}},
		ModelImages:  map[string]bool{"p/m": true},
	}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings", strings.NewReader(`{"theme":"dark","lang":"en"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	s := settings.Load()
	if s.Theme != "dark" || s.ModelNames["p/m"] != "Mine" || len(s.ModelEfforts["p/m"]) != 1 || !s.ModelImages["p/m"] {
		t.Fatalf("theme %q names %v efforts %v images %v", s.Theme, s.ModelNames, s.ModelEfforts, s.ModelImages)
	}
}
