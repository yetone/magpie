package provider

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// releasedHome is a sandbox HOME whose catalog is models.dev's of
// 2026-11-28 (catalog/testdata), which dates the Claude models below.
func releasedHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
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
}

func entryIDs(es []Entry) (out []string) {
	for _, e := range es {
		out = append(out, e.ID)
	}
	return out
}

// The pickers offer each provider's models the newest release first
// (elis_pina on X: an older model added later was listed above the new
// ones). A relay's or a plugin's model whose own list gives no day is
// dated by models.dev's id; two of a day go by name; one nothing dates
// goes after every dated one, in the order its provider listed it (zeta
// before alpha); each provider stays where its first model was, and the
// routing groups keep their order.
func TestNewestFirst(t *testing.T) {
	releasedHome(t)
	relay := &Provider{ID: "relay", Name: "Relay"}
	plugin := &Provider{ID: "cursor", Name: "Cursor"}
	es := []Entry{
		{ID: "group/b", Name: "b", Group: "b", Provider: relay},
		{ID: "relay/claude-opus-4-1", Model: "claude-opus-4-1", Provider: relay},
		{ID: "cursor/zeta", Model: "zeta", Name: "Zeta", Provider: plugin},
		{ID: "relay/my-relay-model", Model: "my-relay-model", Provider: relay},
		{ID: "relay/claude-opus-5-5", Model: "claude-opus-5-5", Provider: relay},
		{ID: "group/a", Name: "a", Group: "a", Provider: plugin},
		{ID: "cursor/alpha", Model: "alpha", Name: "Alpha", Provider: plugin},
		{ID: "cursor/gamma", Model: "gamma", Name: "Gamma", Provider: plugin, Released: "2026-11-27"},
		// a list's own day wins over models.dev's
		{ID: "cursor/claude-sonnet-4-5", Model: "claude-sonnet-4-5", Provider: plugin, Released: "2026-11-27"},
		{ID: "relay/claude-fable-5-2", Model: "claude-fable-5-2", Provider: relay},
		{ID: "relay/anthropic/claude-haiku-5-5", Model: "anthropic/claude-haiku-5-5", Provider: relay},
	}
	was := slices.Clone(es)
	got := entryIDs(NewestFirst(es))
	want := []string{
		"group/b", "group/a",
		"relay/claude-fable-5-2", "relay/anthropic/claude-haiku-5-5", "relay/claude-opus-5-5", "relay/claude-opus-4-1", "relay/my-relay-model",
		"cursor/claude-sonnet-4-5", "cursor/gamma", "cursor/zeta", "cursor/alpha",
	}
	if !slices.Equal(got, want) {
		t.Errorf("NewestFirst\n got %v\nwant %v", got, want)
	}
	if !slices.EqualFunc(es, was, func(a, b Entry) bool { return a.ID == b.ID }) {
		t.Error("NewestFirst reordered the list it was given")
	}
	if d := (Entry{Model: "claude-opus-5-5"}).ReleaseDate(); d != "2026-09-22" {
		t.Errorf("ReleaseDate = %q, want models.dev's 2026-09-22", d)
	}
	if d := (Entry{Group: "g", Model: "claude-opus-5-5"}).ReleaseDate(); d != "" {
		t.Errorf("a routing group's ReleaseDate = %q, want none", d)
	}
}

// An order the user dragged an agent's models into is theirs: its
// pickers keep it, and another agent's still put the newest first.
func TestPickOrderKeepsTheUsersOrder(t *testing.T) {
	releasedHome(t)
	relay := &Provider{ID: "relay", Name: "Relay"}
	es := []Entry{
		{ID: "relay/claude-opus-4-1", Model: "claude-opus-4-1", Provider: relay},
		{ID: "relay/claude-opus-5-5", Model: "claude-opus-5-5", Provider: relay},
	}
	if got := entryIDs(PickOrder("claude", es)); !slices.Equal(got, []string{"relay/claude-opus-5-5", "relay/claude-opus-4-1"}) {
		t.Errorf("no order of the user's: %v, want the newest first", got)
	}
	if err := SetModelOrder("claude", []string{"relay/claude-opus-4-1", "relay/claude-opus-5-5"}); err != nil {
		t.Fatal(err)
	}
	if got := entryIDs(PickOrder("claude", es)); !slices.Equal(got, []string{"relay/claude-opus-4-1", "relay/claude-opus-5-5"}) {
		t.Errorf("the user's order: %v, want it kept", got)
	}
	if got := entryIDs(PickOrder("codex", es)); !slices.Equal(got, []string{"relay/claude-opus-5-5", "relay/claude-opus-4-1"}) {
		t.Errorf("another agent: %v, want the newest first", got)
	}
}
