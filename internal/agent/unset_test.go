package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// An agent nothing is set on reads as nothing set: every field empty, so the
// tray folds it away under "Show {n} more" with the others at their default
// (willz: Gemini CLI, not installed, stayed up the list as its sign-in read
// as Google when none was chosen).
func TestUnsetAgentsReadEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	for _, a := range All() {
		for _, f := range a.Fields {
			if v := f.Get(); v != "" {
				t.Errorf("%s: %s reads %q with nothing set", a.ID, f.Key, v)
			}
		}
	}
}

func TestGeminiAuthUnset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, ".gemini")
	os.MkdirAll(dir, 0o755)
	a := gemini(home)
	f := a.Field("provider")
	// ~/.gemini there (another tool made it), no settings: no sign-in chosen
	if v := f.Get(); v != "" {
		t.Fatalf("no settings: %q", v)
	}
	// settings with no sign-in chosen, only the look
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"ui":{"theme":"Dracula"}}`), 0o644)
	if v := f.Get(); v != "" {
		t.Fatalf("no sign-in chosen: %q", v)
	}
	for auth, want := range map[string]string{"oauth-personal": "google", "gemini-api-key": "api-key", "vertex-ai": "vertex", "cloud-shell": "google"} {
		os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"security":{"auth":{"selectedType":"`+auth+`"}}}`), 0o644)
		if v := f.Get(); v != want {
			t.Fatalf("%s: %q, want %q", auth, v, want)
		}
	}
	// picking Google sets it, and the default takes it away again
	if err := f.Set("google"); err != nil {
		t.Fatal(err)
	}
	if v := f.Get(); v != "google" {
		t.Fatalf("after picking Google: %q", v)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if v := f.Get(); v != "" {
		t.Fatalf("after the default: %q", v)
	}
}
