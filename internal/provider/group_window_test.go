package provider

import (
	"slices"
	"testing"
)

// A model a plugin offers at a context window of its own (Cursor's
// claude-haiku-5-5@1m, grok's grok-4.7@500k) is the same model: it joins
// the group magpie finds for it, listed by the model's own id (Will on
// Discord: ticking claude-haiku-5-5@1m in Cursor left it out of
// auto/claude-haiku-5-5).
func TestAutoGroupsJoinAModelAtItsWindow(t *testing.T) {
	e := func(p, m string) Entry {
		return Entry{ID: p + "/" + m, Model: m, Name: m, Provider: &Provider{ID: p}}
	}
	gs := autoGroups([]Entry{
		e("anthropic", "claude-haiku-5-5"),
		e("cursor-plugin", "claude-haiku-5-5@1m"),
		e("relay", "claude-fable-5.2"),
		e("cursor-plugin", "claude-fable-5-2@1m"),
		e("cursor-plugin", "claude-fable-5-2@300k"),
		e("grok", "grok-4.7@500k"),
		e("grok", "grok-4.7"),
		e("relay", "grok-4.7"),
		e("xai", "grok-4.7@131072"),
		e("vertex", "claude-opus-5-5@20260801"),
		e("relay", "claude-opus-5-5"),
		e("relay", "model@cf"),
		e("other", "model"),
	}, nil)
	got := map[string][]string{}
	spelled := map[string]string{}
	for _, g := range gs {
		got[g.ID] = g.Members
		spelled[g.ID] = g.Spelled
	}
	want := map[string][]string{
		"auto-claude-haiku-5-5": {"anthropic/claude-haiku-5-5", "cursor-plugin/claude-haiku-5-5@1m"},
		// one member a provider: the first size it lists
		"auto-claude-fable-5-2": {"relay/claude-fable-5.2", "cursor-plugin/claude-fable-5-2@1m"},
		// a provider's unsized model over its sized one
		"auto-grok-4-7":        {"grok/grok-4.7", "relay/grok-4.7", "xai/grok-4.7@131072"},
		"auto-claude-opus-5-5": {"vertex/claude-opus-5-5@20260801", "relay/claude-opus-5-5"},
	}
	if len(got) != len(want) {
		t.Fatalf("groups %v, want %v", got, want)
	}
	for id, ms := range want {
		if !slices.Equal(got[id], ms) {
			t.Errorf("%s: members %v, want %v", id, got[id], ms)
		}
	}
	// listed by the model, never by a window
	if s := spelled["auto-claude-haiku-5-5"]; s != "claude-haiku-5-5" {
		t.Errorf("auto-claude-haiku-5-5 spelled %q", s)
	}
	only := autoGroups([]Entry{e("cursor-plugin", "claude-opus-5-5@1m"), e("cursor-b", "claude-opus-5-5@300k")}, nil)
	if len(only) != 1 || only[0].Spelled != "claude-opus-5-5" {
		t.Errorf("a group of sized members only: %+v", only)
	}
	// what the Same as box shows a sized model merging by
	for in, want := range map[string]string{"claude-haiku-5-5@1m": "claude-haiku-5-5", "Grok-4.7@500K": "grok-4-7", "claude-opus-5-5@20260801": "claude-opus-5-5", "model@cf": "model@cf"} {
		if got := MergeName(Provider{ID: "cursor-plugin"}, in); got != want {
			t.Errorf("MergeName(%q) = %q, want %q", in, got, want)
		}
	}
}
