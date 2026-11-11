package provider

import (
	"slices"
	"testing"
)

func TestCleanModelTier(t *testing.T) {
	for in, want := range map[string]string{"": "", "usual": "", "Fast": "priority", "priority": "priority", "ultra": "ultrafast", " ultrafast ": "ultrafast"} {
		if got, err := CleanModelTier(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	if _, err := CleanModelTier("turbo"); err == nil {
		t.Error("turbo accepted")
	}
}

// A group's Ultra is a part of its Fast: saving, renaming and taking a
// member out keep them so (kdajenks on X).
func TestSaveGroupUltra(t *testing.T) {
	fastHome(t)
	if err := SaveGroup(Group{ID: "u", Members: []string{"oa/gpt-6.1-sol:high", "an/claude-opus-5-5"},
		Fast: []string{"oa/gpt-6.1-sol:high"}, Ultra: []string{"oa/gpt-6.1-sol:high", "an/claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
	g, ms, _ := FindGroup("group/u")
	if !slices.Equal(g.Ultra, []string{"oa/gpt-6.1-sol:high"}) {
		t.Fatalf("ultra %v: a member that isn't fast kept it", g.Ultra)
	}
	if len(ms) != 2 || !ms[0].Ultra || !ms[0].Fast || ms[1].Ultra {
		t.Fatalf("resolved %+v", ms)
	}
	g.RenameMember("oa/gpt-6.1-sol:high", "oa/gpt-6.1-sol:low")
	if !g.IsUltra("oa/gpt-6.1-sol:low") || g.IsUltra("oa/gpt-6.1-sol:high") {
		t.Fatalf("renamed %v", g.Ultra)
	}
	g.SetMemberTier("oa/gpt-6.1-sol:low", "priority")
	if g.IsUltra("oa/gpt-6.1-sol:low") || !g.IsFast("oa/gpt-6.1-sol:low") {
		t.Fatalf("back to fast: %v %v", g.Fast, g.Ultra)
	}
	g.SetMemberTier("oa/gpt-6.1-sol:low", "ultrafast")
	g.SetMemberFast("oa/gpt-6.1-sol:low", false)
	if g.IsFast("oa/gpt-6.1-sol:low") || len(g.Ultra) != 0 {
		t.Fatalf("off: %v %v", g.Fast, g.Ultra)
	}
	if err := Rename("oa", "openai"); err != nil {
		t.Fatal(err)
	}
	if g, _, _ = FindGroup("group/u"); !slices.Contains(g.Ultra, "openai/gpt-6.1-sol:high") {
		t.Fatalf("after the provider's rename %v", g.Ultra)
	}
}
