package provider

import (
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// The groups magpie finds merge a model however a vendor spells its id:
// Volcengine Ark's dated deepseek-v4-1-flash-260910 is ali's
// deepseek-v4.1-flash (kyzhouxu, #583). Models that differ stay apart.
func TestSameModelVendorSpellings(t *testing.T) {
	for in, want := range map[string]string{
		// Ark's YYMMDD snapshot
		"deepseek-v4-1-flash-260910":      "deepseek-v4-1-flash",
		"deepseek-v4.1-flash":             "deepseek-v4-1-flash",
		"DeepSeek-V4.1-Flash":             "deepseek-v4-1-flash",
		"deepseek-ai/DeepSeek-V4.1-Flash": "deepseek-v4-1-flash",
		"doubao-seed-1-6-250615":          "doubao-seed-1-6",
		"doubao-seed-1.6-thinking-250715": "doubao-seed-1-6-thinking",
		// Fireworks: its path, and "p" for the dot
		"accounts/fireworks/models/deepseek-v4p1-flash": "deepseek-v4-1-flash",
		"accounts/fireworks/models/qwen2p5-72b":         "qwen2-5-72b",
		// "_" for "-", and Vertex's "@" date
		"deepseek_v4_1_flash":      "deepseek-v4-1-flash",
		"claude-opus-5-5@20260801": "claude-opus-5-5",
		// not a date: kept
		"deepseek-v4-1-flash-128000": "deepseek-v4-1-flash-128000",
		"m-221301":                   "m-221301",
		"m-261301":                   "m-261301",
		"m-260100":                   "m-260100",
		"qwen3-235b-a22b-2507":       "qwen3-235b-a22b-2507",
		"kimi-k2-0905":               "kimi-k2-0905",
		"gpt-4o":                     "gpt-4o",
		"o3-pro":                     "o3-pro",
		"llama-3p":                   "llama-3p",
	} {
		if got := sameModel(in); got != want {
			t.Errorf("sameModel(%q) = %q, want %q", in, got, want)
		}
	}
	// genuinely different models: never one group
	for _, pair := range [][2]string{
		{"deepseek-v4-1-flash-260910", "deepseek-v4.1"},
		{"deepseek-v4-1-260910", "deepseek-v4.1-flash"},
		{"deepseek-v4.1-thinking", "deepseek-v4.1"},
		{"qwen3-235b-a22b-2507", "qwen3-235b-a22b"},
		{"claude-opus-5.5:batch", "claude-opus-5.5"},
		{"deepseek-v4.1-flash", "deepseek-v4.11-flash"},
	} {
		if sameModel(pair[0]) == sameModel(pair[1]) {
			t.Errorf("%s and %s merged as %s", pair[0], pair[1], sameModel(pair[0]))
		}
	}
	e := func(p, m string) Entry { return Entry{ID: p + "/" + m, Model: m, Name: m, Provider: &Provider{ID: p}} }
	gs := autoGroups([]Entry{e("volcengine", "deepseek-v4-1-flash-260910"), e("ali-tp", "deepseek-v4.1-flash")}, nil)
	if len(gs) != 1 || gs[0].ID != "auto-deepseek-v4-1-flash" || !slices.Equal(gs[0].Members, []string{"volcengine/deepseek-v4-1-flash-260910", "ali-tp/deepseek-v4.1-flash"}) {
		t.Fatalf("groups: %+v", gs)
	}
}

// A model whose id no rule matches up with another vendor's is merged
// with it once the user says, in the provider's Model settings, which
// model it is the same as (#583): saved with the provider's Save, kept in
// settings' ModelSameAs, and taken by the groups magpie finds.
func TestModelSameAsMergesFoundGroup(t *testing.T) {
	prefsHome(t)
	touched := 0
	catalog.Changed = func() { touched++ }
	t.Cleanup(func() { catalog.Changed = nil })
	if err := Save(Provider{ID: "c", Name: "C", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"ep-sol-x"}}); err != nil {
		t.Fatal(err)
	}
	members := func(gid string) []string {
		_, ms, ok := FindGroup(GroupPrefix + gid)
		if !ok {
			return nil
		}
		var out []string
		for _, m := range ms {
			out = append(out, m.ID)
		}
		return out
	}
	if got := members("auto-sol"); !slices.Equal(got, []string{"a/sol", "b/sol"}) {
		t.Fatalf("before: %v", got)
	}
	touched = 0
	same := "Vendor/SOL"
	if err := SetModelPrefs("c", map[string]ModelPref{"ep-sol-x": {Same: &same}}); err != nil {
		t.Fatal(err)
	}
	if touched != 1 || settings.Load().ModelSameAs["c/ep-sol-x"] != "Vendor/SOL" {
		t.Fatalf("told %d, saved %v", touched, settings.Load().ModelSameAs)
	}
	if got := members("auto-sol"); !slices.Equal(got, []string{"a/sol", "b/sol", "c/ep-sol-x"}) {
		t.Fatalf("after: %v", got)
	}
	if AutoGroupOf("c", "ep-sol-x") != "auto-sol" || AutoGroupID("ep-sol-x") != "auto-ep-sol-x" {
		t.Fatalf("AutoGroupOf %s", AutoGroupOf("c", "ep-sol-x"))
	}
	if g, ok := GroupFor("sol"); !ok || g != GroupPrefix+"auto-sol" {
		t.Fatalf("GroupFor: %s %v", g, ok)
	}
	// the same again: nothing to tell
	if err := SetModelPrefs("c", map[string]ModelPref{"ep-sol-x": {Same: &same}}); err != nil || touched != 1 {
		t.Fatalf("%v, told %d", err, touched)
	}
	// its own id however spelt is no mapping: nothing kept
	own := "c/EP_Sol-X"
	if err := SetModelPrefs("c", map[string]ModelPref{"ep-sol-x": {Same: &own}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := settings.Load().ModelSameAs["c/ep-sol-x"]; ok || touched != 2 {
		t.Fatalf("kept %v, told %d", settings.Load().ModelSameAs, touched)
	}
	if got := members("auto-sol"); !slices.Equal(got, []string{"a/sol", "b/sol"}) {
		t.Fatalf("restored: %v", got)
	}
	// a model the provider doesn't serve, or a name of no model, is refused
	if err := SetModelPrefs("c", map[string]ModelPref{"nope": {Same: &same}}); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("got %v", err)
	}
	junk := "--/"
	if err := SetModelPrefs("c", map[string]ModelPref{"ep-sol-x": {Same: &junk}}); err == nil {
		t.Fatal("a name of no model was taken")
	}
}

// A provider added "Only through routing groups" whose relay lists a
// model by its display name ("GPT-6 Sol") joins the group magpie found
// for gpt-6-sol, as a tidy Claude id joins its own (Chill on Discord).
// Spaces, "_" and "-" are one gap; the words in them still count, so
// gpt-6 and its -mini or -sol stay apart, and so does OpenAI's dated
// snapshot from the alias.
func TestSameModelDisplayNameAsID(t *testing.T) {
	for in, want := range map[string]string{
		"GPT-6 Sol":          "gpt-6-sol",
		"GPT 6 Sol":          "gpt-6-sol",
		"gpt-6  sol":         "gpt-6-sol",
		"GPT_6 - Sol":        "gpt-6-sol",
		"\tGPT-6 Sol ":       "gpt-6-sol",
		"relay/GPT-6.1 Sol":  "gpt-6-1-sol",
		"GPT-6 Sol 20260901": "gpt-6-sol",
		"Claude Opus 5.5":    "claude-opus-5-5",
		"gpt-4o-2024-08-06":  "gpt-4o-2024-08-06",
	} {
		if got := sameModel(in); got != want {
			t.Errorf("sameModel(%q) = %q, want %q", in, got, want)
		}
	}
	for _, pair := range [][2]string{
		{"GPT-6 Sol", "gpt-6"},
		{"GPT-6", "gpt-6-sol"},
		{"GPT-6 Mini", "gpt-6"},
		{"GPT-6 Sol Mini", "gpt-6-sol"},
		{"GPT-6 Sol", "gpt-6-sol:batch"},
		{"gpt-4o-2024-08-06", "gpt-4o"},
	} {
		if sameModel(pair[0]) == sameModel(pair[1]) {
			t.Errorf("%s and %s merged as %s", pair[0], pair[1], sameModel(pair[0]))
		}
	}

	prefsHome(t)
	if err := Save(Provider{ID: "x", Name: "X", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"gpt-6-sol", "gpt-6-mini"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "y", Name: "Y", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"GPT-6 Sol", "GPT 6", "GPT-6 Sol Mini"}, Unlisted: true}); err != nil {
		t.Fatal(err)
	}
	_, ms, ok := FindGroup(GroupPrefix + "auto-gpt-6-sol")
	var got []string
	for _, m := range ms {
		got = append(got, m.ID)
	}
	if !ok || !slices.Equal(got, []string{"x/gpt-6-sol", "y/GPT-6 Sol"}) {
		t.Fatalf("auto-gpt-6-sol: %v %v", got, ok)
	}
	if g := AutoGroupOf("y", "GPT-6 Sol"); g != "auto-gpt-6-sol" {
		t.Fatalf("AutoGroupOf: %s", g)
	}
	if g, ok := GroupFor("GPT-6 Sol"); !ok || g != GroupPrefix+"auto-gpt-6-sol" {
		t.Fatalf("GroupFor: %s %v", g, ok)
	}
	for _, g := range Groups() {
		if g.Auto && g.ID != "auto-gpt-6-sol" && g.ID != "auto-sol" {
			t.Errorf("a different model merged: %s %v", g.ID, g.Members)
		}
	}
}
