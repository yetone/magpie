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
	// A pick already saved whose provider stopped serving it is not refused
	// when the page sends it back. prefsKeep re-sends the page's own picks
	// with every save — a theme, a language, the tray — so refusing a stale
	// one answered 400 to every setting on the page, and a user whose vision
	// model a vendor retired could not change anything at all until they
	// cleared a pick they could no longer see.
	for _, stale := range []struct {
		name, field, value string
		saved              func(settings.Settings) string
	}{
		{"image recognition", "vision", "fake/missing", func(s settings.Settings) string { return s.Vision }},
		{"image generation", "imageGen", "fake/missing", func(s settings.Settings) string { return s.ImageGen }},
	} {
		s := settings.Load()
		// the pick as it was made, before the vendor retired the model: it is
		// written straight to disk, since Save's own check is only that it
		// looks like a model's id
		switch stale.field {
		case "vision":
			s.Vision = stale.value
		case "imageGen":
			s.ImageGen = stale.value
		}
		if err := settings.Save(s); err != nil {
			t.Fatal(err)
		}
		if got := stale.saved(settings.Load()); got != stale.value {
			t.Fatalf("%s before the save: %q, want %q", stale.name, got, stale.value)
		}
		// the page sends its pick back with a change the user just made
		body := fmt.Sprintf(`{"theme":"dark",%q:%q}`, stale.field, stale.value)
		if code, _ := call("/api/settings", body); code != 200 {
			t.Errorf("%s: a save that re-sent its own pick answered %d", stale.name, code)
		}
		if got := settings.Load().Theme; got != "dark" {
			t.Errorf("%s: the theme with it was not saved: %q", stale.name, got)
		}
		if got := stale.saved(settings.Load()); got != stale.value {
			t.Errorf("%s: the pick was overwritten: %q", stale.name, got)
		}
		// a pick just made is still checked: this is what the guard is for
		if code, _ := call("/api/settings", fmt.Sprintf(`{%q:%q}`, stale.field, "fake/other-missing")); code < 400 {
			t.Errorf("%s: accepted a model magpie doesn't serve: %d", stale.name, code)
		}
	}
	if got := settings.Load().Theme; got != "dark" {
		t.Errorf("theme after the stale saves: %q, want dark", got)
	}
}
