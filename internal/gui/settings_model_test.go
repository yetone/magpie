package gui

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// A model the Settings pick has to be one magpie serves: Codex's thread
// titles (#705), the one that describes images to a model that can't see
// them, and the one Magpie Image draws with. provider.Resolve answers for
// any model spelled under a provider that is on, so "fake/missing" and
// "fake/" were saved as though they were models — every image description,
// every drawing and every thread title then went to the vendor on a model it
// doesn't list, and Codex drops a title it failed to write, so the user was
// left with a thread that has no title at all. The usual cause is a real
// model picked first and its provider renaming or retiring it afterwards.
func TestSettingsModelMustBeServed(t *testing.T) {
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
	// m1-image is a model the provider draws with: the image generation
	// picker lists gateway.Drawers', which isn't the served catalog's
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1", "m1-image"}, Chat: "http://127.0.0.1:9/v1"}); err != nil {
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

	for _, c := range []struct {
		name, path, body, model string
		saved                   func(settings.Settings) string
	}{
		{"Codex's titles", "/api/settings/codex-titles", `{"model":"%s"}`, "fake/m1",
			func(s settings.Settings) string { return s.CodexTitles }},
		{"image recognition", "/api/settings", `{"vision":"%s"}`, "fake/m1",
			func(s settings.Settings) string { return s.Vision }},
		{"image generation", "/api/settings", `{"imageGen":"%s"}`, "fake/m1-image",
			func(s settings.Settings) string { return s.ImageGen }},
	} {
		if code, _ := call(c.path, fmt.Sprintf(c.body, c.model)); code != 200 {
			t.Fatalf("%s refused %s: %d", c.name, c.model, code)
		}
		keep := c.saved(settings.Load())
		for _, bad := range []string{"fake/missing", "fake/"} {
			if code, _ := call(c.path, fmt.Sprintf(c.body, bad)); code < 400 {
				t.Errorf("%s accepted %s: %d", c.name, bad, code)
			}
			if got := c.saved(settings.Load()); got != keep {
				t.Errorf("%s rejected %s and left %q, not %q", c.name, bad, got, keep)
			}
		}
		// "" is back to magpie's own pick and "off" turns it off, both kept
		for _, good := range []string{"off", ""} {
			if code, _ := call(c.path, fmt.Sprintf(c.body, good)); code != 200 {
				t.Errorf("%s refused %q: %d", c.name, good, code)
			}
		}
	}
	if s := settings.Load(); s.CodexTitles != "" || s.Vision != "" || s.ImageGen != "" {
		t.Errorf("each setting after its empty pick: %q, %q, %q", s.CodexTitles, s.Vision, s.ImageGen)
	}
	// image generation's picker offers what a provider draws with, not every
	// model it serves: a model that only chats is refused there, since
	// nothing draws with it
	if code, _ := call("/api/settings", `{"imageGen":"fake/m1"}`); code < 400 {
		t.Errorf("image generation accepted fake/m1, which draws nothing: %d", code)
	}
	// the page sends its own picks with every save, so a value already saved
	// is checked again rather than kept: its provider may have stopped
	// serving it since
	s := settings.Load()
	s.Vision = "fake/missing"
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if code, _ := call("/api/settings", `{"theme":"dark","vision":"fake/missing"}`); code < 400 {
		t.Errorf("a save that re-sent the stale image recognition model: %d", code)
	}
	if got := settings.Load().Vision; got != "fake/missing" {
		t.Errorf("the stale image recognition model was overwritten: %q", got)
	}
}
