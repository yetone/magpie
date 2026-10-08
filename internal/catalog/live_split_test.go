package catalog

import (
	"path/filepath"
	"slices"
	"testing"
)

// A provider whose protocols are served at bases of their own is asked each
// of them, and the parts are kept beside the merged list: a base that can't
// be asked keeps the models it listed last time (#904). A base that listed
// nothing is kept as an empty part of its own, which is what tells a later
// fetch that it has no models rather than that its part was lost.
func TestLiveSplitKeepsEachEndpoint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	chat := []Model{{ID: "gpt-a"}, {ID: "shared"}}
	anthropic := []Model{{ID: "claude-b"}, {ID: "shared"}, {ID: "gem", Draws: true}}
	all := []Model{{ID: "gpt-a"}, {ID: "shared"}, {ID: "claude-b"}, {ID: "gem", Draws: true}}
	if err := SaveLiveSides("split", "https://chat/v1", all, map[string][]Model{
		"https://chat/v1":      chat,
		"https://anthropic/v1": anthropic,
		"https://quiet/v1":     {},
	}); err != nil {
		t.Fatal(err)
	}
	sides, last, ok := LiveSplit("split")
	if !ok {
		t.Fatal("no list saved")
	}
	if len(sides) != 3 {
		t.Fatalf("parts %v, want one per endpoint", sides)
	}
	if got := ids(sides["https://chat/v1"]); !slices.Equal(got, []string{"gpt-a", "shared"}) {
		t.Errorf("chat's part %v", got)
	}
	if got := ids(sides["https://anthropic/v1"]); !slices.Equal(got, []string{"claude-b", "shared", "gem"}) {
		t.Errorf("anthropic's part %v", got)
	}
	if part, had := sides["https://quiet/v1"]; !had || len(part) != 0 {
		t.Errorf("the base that listed nothing: %v %v", part, had)
	}
	if got := ids(last); !slices.Equal(got, []string{"gpt-a", "shared", "claude-b", "gem"}) {
		t.Errorf("whole list %v", got)
	}
	// the list the product reads is the merged one, without the drawer
	if live, _, ok := Live("split"); !ok || !slices.Equal(ids(live), []string{"gpt-a", "shared", "claude-b"}) {
		t.Errorf("live %v %v", ids(live), ok)
	}

	// a list saved without parts is one every endpoint answered alike
	if err := SaveLive("whole", "https://one/v1", []Model{{ID: "gpt-a"}}); err != nil {
		t.Fatal(err)
	}
	if sides, last, ok := LiveSplit("whole"); !ok || sides != nil || len(last) != 1 {
		t.Errorf("parts %v, list %v, ok %v", sides, ids(last), ok)
	}
	// and a provider with no list at all has neither
	if _, _, ok := LiveSplit("nosuch"); ok {
		t.Error("a list was found for a provider that has none")
	}
}

func ids(ms []Model) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}
