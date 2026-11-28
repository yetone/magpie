package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The days models.dev says models came out, for the lists that put the
// newest first (elis_pina on X: 根据模型的推出时间进行排序). The testdata is
// models.dev's api.json of 2026-11-28 (SHA-256 of the whole response:
// ebf491b21dcfa4fe03054552a6ea7512b3d929cb58bf577875112c2237839793), its
// anthropic provider whole and Azure's and OpenCode's entries of two
// older Claude models as they were, so the votes are real: Azure dates
// claude-sonnet-4-5 2025-11-18, the day it served it, and the two others
// 2025-09-29.
func TestReleasedOf(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "models.dev-2026-11-28-released.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeCatalog(t, string(body))
	for _, c := range []struct{ id, want string }{
		{"claude-opus-5-5", "2026-09-22"},
		{"claude-fable-5-2", "2026-11-09"},
		// a relay's or a plugin's id for it, as SeesImages finds it
		{"anthropic/claude-opus-5-5", "2026-09-22"},
		{"Claude-Haiku-5-5", "2026-10-07"},
		// most of those serving it, not the one that dates it apart
		{"claude-sonnet-4-5", "2025-09-29"},
		// one only OpenCode lists
		{"claude-opus-4-1", "2025-08-05"},
		// one models.dev doesn't know has no date
		{"my-relay-model", ""},
	} {
		if got := ReleasedOf(c.id); got != c.want {
			t.Errorf("ReleasedOf(%q) = %q, want %q", c.id, got, c.want)
		}
	}
}

// A tie between two dates goes to the earlier, whichever is read first.
func TestMostDatedTie(t *testing.T) {
	for range 20 {
		if got := mostDated(map[string]int{"2025-11-18": 1, "2025-09-29": 1}); got != "2025-09-29" {
			t.Fatalf("mostDated = %q, want the earlier", got)
		}
	}
}

// Settings' image and video pickers put a provider's newest model first:
// its list's own day, else models.dev's; two of a day by name; the undated
// after them in the provider's own order (zeta before alpha).
func TestNewestFirstModels(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "models.dev-2026-11-28-released.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeCatalog(t, string(body))
	ms := []Model{
		{ID: "zeta"}, {ID: "claude-opus-4-1"}, {ID: "alpha"}, {ID: "gamma", Released: "2026-11-09"},
		{ID: "claude-fable-5-2"}, {ID: "beta", Released: "2026-12-01"},
	}
	var got []string
	for _, m := range NewestFirst(ms) {
		got = append(got, m.ID)
	}
	want := []string{"beta", "claude-fable-5-2", "gamma", "claude-opus-4-1", "zeta", "alpha"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("NewestFirst\n got %v\nwant %v", got, want)
	}
	if ms[0].ID != "zeta" {
		t.Error("NewestFirst reordered the list it was given")
	}
}
