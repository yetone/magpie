package gui

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Settings' Codex thread titles (#705) is set on its own: off, a model
// magpie serves (one it doesn't is refused), or back to Codex's own; the
// Settings page's other saves, which never send it, keep it; and the page
// is told the models it may name.
func TestCodexTitlesSetting(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("APPDATA", filepath.Join(h, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(h, "AppData", "Local"))
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Chat: "http://127.0.0.1:9/v1"}); err != nil {
		t.Fatal(err)
	}
	call := func(path, body string) (int, map[string]any) {
		t.Helper()
		rec := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	code, out := call("/api/settings/codex-titles", `{"model":"off"}`)
	if code != 200 || out["codexTitles"] != "off" || settings.Load().CodexTitles != "off" {
		t.Fatalf("off: %d %v", code, out["codexTitles"])
	}
	found := false
	for _, m := range out["titleModels"].([]any) {
		if m.(map[string]any)["id"] == "fake/m1" {
			found = true
		}
	}
	if !found {
		t.Errorf("titleModels %v has no fake/m1", out["titleModels"])
	}
	if code, _ := call("/api/settings", `{"theme":"dark"}`); code != 200 || settings.Load().CodexTitles != "off" {
		t.Errorf("another save (%d) lost the setting: %q", code, settings.Load().CodexTitles)
	}
	if code, _ := call("/api/settings/codex-titles", `{"model":"nope/x"}`); code < 400 || settings.Load().CodexTitles != "off" {
		t.Errorf("a model magpie doesn't serve: %d, %q", code, settings.Load().CodexTitles)
	}
	if code, out := call("/api/settings/codex-titles", `{"model":"fake/m1"}`); code != 200 || out["codexTitles"] != "fake/m1" {
		t.Errorf("a model: %d %v", code, out["codexTitles"])
	}
	if code, _ := call("/api/settings/codex-titles", `{"model":""}`); code != 200 || settings.Load().CodexTitles != "" {
		t.Errorf("Codex's own: %d %q", code, settings.Load().CodexTitles)
	}
}
