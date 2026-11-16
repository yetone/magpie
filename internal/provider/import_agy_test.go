package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

func agyImportHome(t *testing.T, settings string) {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)                                  // Windows
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming")) // Windows: never the real Alma
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_GEMINI_BASE_URL", "")
	if settings != "" {
		dir := filepath.Join(home, ".gemini", "antigravity-cli")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// loginShell makes the login shell answer vars, as a terminal's agy has
// them while magpie, opened from the Dock, doesn't.
func loginShell(t *testing.T, vars map[string]string) {
	t.Helper()
	agyLoginEnv = func(k string) string { return vars[k] }
}

func agyDiscovered(t *testing.T) (AppSource, bool) {
	t.Helper()
	for _, s := range DiscoverSources() {
		if s.ID == "agy" {
			return s, true
		}
	}
	return AppSource{}, false
}

// shenggxhz on X: agy set up on an AI Studio key ("modelProvider":
// "gemini" in its settings.json, GEMINI_API_KEY exported in the shell)
// lists Gemini 4 Carbon and Hydrogen, and nothing in magpie served that
// key. The Providers page finds it by itself, as the Google Gemini
// provider, and imports it as one.
func TestAgyGeminiKeyIsFound(t *testing.T) {
	agyImportHome(t, "{\n  // the reporter's\n  \"modelProvider\": \"gemini\"\n}\n")
	loginShell(t, map[string]string{"GEMINI_API_KEY": "AIzaSy-test-key"})
	s, ok := agyDiscovered(t)
	if !ok || !s.Found || s.Error != "" || len(s.Items) != 1 {
		t.Fatalf("agy's key isn't offered: %+v", s)
	}
	it := s.Items[0]
	if it.Skip != "" || it.Off != "" || it.Status != "new" {
		t.Fatalf("offered as %+v, want new", it)
	}
	if p := it.Provider; p.Preset != "google" || p.Catalog != "google" || p.Key != "AIzaSy-test-key" {
		t.Fatalf("provider %+v, want Google Gemini on the key", p)
	}
	added, err := ImportFromApps([]AppPick{{Source: "agy", Ref: it.Ref}})
	if err != nil || len(added) != 1 {
		t.Fatalf("import: %v %v", added, err)
	}
	var got *Provider
	for _, p := range load().Providers {
		if p.Preset == "google" {
			got = &p
		}
	}
	if got == nil || got.Key != "AIzaSy-test-key" {
		t.Fatalf("after import: %+v", load().Providers)
	}
	if s, _ := agyDiscovered(t); len(s.Items) != 1 || s.Items[0].Status != "same" {
		t.Fatalf("after import it is still offered: %+v", s.Items)
	}
}

// magpie's own environment comes first, as agy started from it has it.
func TestAgyGeminiKeyFromMagpiesEnv(t *testing.T) {
	agyImportHome(t, `{"modelProvider": "gemini"}`)
	loginShell(t, nil)
	t.Setenv("GEMINI_API_KEY", "AIzaSy-env-key")
	s, _ := agyDiscovered(t)
	if len(s.Items) != 1 || s.Items[0].Provider.Key != "AIzaSy-env-key" {
		t.Fatalf("%+v", s.Items)
	}
}

// agy on its Google sign-in is the agy-cli subscription's, not a key's:
// nothing is offered, a GEMINI_API_KEY in the shell for another program
// included. Nor is a key with no setting, or one at magpie itself.
func TestAgyGeminiKeyOnlyWhenAgyUsesIt(t *testing.T) {
	for name, c := range map[string]struct {
		settings string
		vars     map[string]string
		skip     bool
	}{
		"signed in":      {`{"model": "gemini-4-argon-high"}`, map[string]string{"GEMINI_API_KEY": "AIzaSy-other"}, false},
		"other provider": {`{"modelProvider": "vertex"}`, map[string]string{"GEMINI_API_KEY": "AIzaSy-other"}, false},
		"empty":          {" ", map[string]string{"GEMINI_API_KEY": "AIzaSy-other"}, false},
		"no key":         {`{"modelProvider": "gemini"}`, nil, true},
		"magpie":         {`{"modelProvider": "gemini"}`, map[string]string{"GEMINI_API_KEY": "mp-agy", "GOOGLE_GEMINI_BASE_URL": "magpie"}, true},
	} {
		t.Run(name, func(t *testing.T) {
			agyImportHome(t, c.settings)
			if c.vars["GOOGLE_GEMINI_BASE_URL"] == "magpie" {
				c.vars["GOOGLE_GEMINI_BASE_URL"] = "http://" + settings.GatewayAddr()
			}
			loginShell(t, c.vars)
			s, _ := agyDiscovered(t)
			if c.skip {
				if len(s.Items) != 1 || s.Items[0].Skip == "" {
					t.Fatalf("want one skipped: %+v", s.Items)
				}
				return
			}
			if len(s.Items) != 0 {
				t.Fatalf("offered: %+v", s.Items)
			}
		})
	}
}

// A relay agy is pointed at is imported at its Gemini endpoint.
func TestAgyGeminiKeyAtRelay(t *testing.T) {
	agyImportHome(t, `{"modelProvider": "gemini"}`)
	loginShell(t, map[string]string{"GEMINI_API_KEY": "sk-relay", "GOOGLE_GEMINI_BASE_URL": "https://relay.example.com/gemini"})
	s, _ := agyDiscovered(t)
	if len(s.Items) != 1 || s.Items[0].Skip != "" || s.Items[0].Provider.Gemini != "https://relay.example.com/gemini/v1beta" {
		t.Fatalf("%+v", s.Items)
	}
}
