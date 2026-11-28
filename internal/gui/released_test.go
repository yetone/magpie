package gui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// releasedHome is a sandbox HOME with models.dev's catalog of 2026-11-28
// (catalog/testdata) and a relay listing four models in the order they
// were added, the oldest Claude last but one and one models.dev doesn't
// know.
func releasedHome(t *testing.T) provider.Provider {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	body, err := os.ReadFile(filepath.Join("..", "catalog", "testdata", "models.dev-2026-11-28-released.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), body, 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	p := provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1",
		Models: []string{"claude-opus-5-5", "claude-opus-4-1", "my-own", "claude-fable-5-2"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return p
}

var releasedWant = map[string]string{
	"claude-opus-5-5":  "2026-09-22",
	"claude-opus-4-1":  "2025-08-05",
	"my-own":           "",
	"claude-fable-5-2": "2026-11-09",
}

// Every list the window draws a model picker from says the day each model
// came out, by models.dev's id where the relay's list gives none, for the
// page to put the newest first (elis_pina on X: 根据模型的推出时间进行排序):
// the Providers page's editor, an agent's model list and the Routing
// page's pickers.
func TestModelListsSayTheReleaseDay(t *testing.T) {
	p := releasedHome(t)
	got := map[string]string{}
	for _, m := range providerInfo(p, nil).Models {
		got[m.ID] = m.Released
	}
	for id, want := range releasedWant {
		if got[id] != want {
			t.Errorf("Providers: %s released %q, want %q", id, got[id], want)
		}
	}
	a, err := agent.Find("claude")
	if err != nil {
		t.Fatal(err)
	}
	got = map[string]string{}
	for _, m := range agentModelList(a) {
		got[m.ID] = m.Released
	}
	for id, want := range releasedWant {
		if got["relay/"+id] != want {
			t.Errorf("an agent's list: relay/%s released %q, want %q", id, got["relay/"+id], want)
		}
	}
	got = map[string]string{}
	for _, m := range groupsState().Models {
		got[m.ID] = m.Released
	}
	for id, want := range releasedWant {
		if got["relay/"+id] != want {
			t.Errorf("Routing: relay/%s released %q, want %q", id, got["relay/"+id], want)
		}
	}
}

// Settings' model pickers (a title's, masking's, a side job's) offer each
// provider's models the newest first, an undated one last.
func TestSettingsPickersPutTheNewestFirst(t *testing.T) {
	releasedHome(t)
	want := []string{"relay/claude-fable-5-2", "relay/claude-opus-5-5", "relay/claude-opus-4-1", "relay/my-own"}
	relay := func(ms []modelRef) (out []string) {
		for _, m := range ms {
			if strings.HasPrefix(m.ID, "relay/") {
				out = append(out, m.ID)
			}
		}
		return out
	}
	if got := relay(settingsState().TitleModels); !slices.Equal(got, want) {
		t.Errorf("titles\n got %v\nwant %v", got, want)
	}
	if got := relay(servedModels()); !slices.Equal(got, want) {
		t.Errorf("side jobs\n got %v\nwant %v", got, want)
	}
}
