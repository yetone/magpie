package provider

import (
	"maps"
	"slices"
	"testing"
)

// Vendors spell the same model differently; a group magpie finds joins
// them all.
func TestAutoGroupsSameModel(t *testing.T) {
	for in, want := range map[string]string{
		"claude-opus-5-5":                 "claude-opus-5-5",
		"anthropic/claude-opus-5.5":       "claude-opus-5-5",
		"Claude-Opus-5.5":                 "claude-opus-5-5",
		"claude-opus-5-5-20260801":        "claude-opus-5-5",
		"anthropic/claude-opus-5.5:batch": "claude-opus-5-5:batch",
		"gpt-5.1-codex":                   "gpt-5-1-codex",
		"v1.beta":                         "v1.beta",
	} {
		if got := sameModel(in); got != want {
			t.Errorf("sameModel(%q) = %q, want %q", in, got, want)
		}
	}
	e := func(p, m, name string) Entry {
		return Entry{ID: p + "/" + m, Model: m, Name: name, Provider: Provider{ID: p}}
	}
	gs := autoGroups([]Entry{
		e("openrouter", "anthropic/claude-opus-5.5", "anthropic/claude-opus-5.5"),
		e("copilot", "claude-opus-5.5", "claude-opus-5.5"),
		e("claude", "claude-opus-5-5", "Claude Opus 5.5"),
	}, nil)
	if len(gs) != 1 || gs[0].ID != "auto-claude-opus-5-5" || len(gs[0].Members) != 3 || gs[0].Name != "Claude Opus 5.5" {
		t.Fatalf("groups: %+v", gs)
	}

}

// A group takes images when one of its members does: the gateway gives an
// image to the members that see it (#756: an auto group of DeepSeek V4.1
// Flash with one text-only member turned every image away).
func TestGroupImages(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	no, yes := false, true
	e := func(p, m string, images bool, in *bool) Entry {
		return Entry{ID: p + "/" + m, Model: m, Name: m, Provider: Provider{ID: p}, Images: images, ImageInput: in}
	}
	gs := groupEntries([]Entry{
		e("a", "kimi-k3", true, nil), e("b", "kimi-k3", true, nil),
		e("a", "glm-5.3", true, nil), e("b", "glm-5.3", false, nil),
		e("a", "deepseek-v4.1-flash", false, &no), e("b", "deepseek-v4.1-flash", true, &yes),
		e("a", "glm-5.2", false, &no), e("b", "glm-5.2", false, &no),
	})
	got := map[string]bool{}
	for _, g := range gs {
		got[g.ID] = g.Images
	}
	want := map[string]bool{
		AutoPrefix + "kimi-k3": true, AutoPrefix + "glm-5.3": true,
		AutoPrefix + "deepseek-v4.1-flash": true, AutoPrefix + "glm-5.2": false,
	}
	if !maps.Equal(got, want) {
		t.Errorf("%v", got)
	}
}

func TestGroupImagesFromAMemberThatSees(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	yes := true
	entries := []Entry{
		{ID: "a/m", Model: "m", Provider: Provider{ID: "a"}, Images: true, ImageInput: &yes},
		{ID: "b/m", Model: "m", Provider: Provider{ID: "b"}, Images: true},
	}
	groups := groupEntries(entries)
	if len(groups) != 1 || !groups[0].Images || groups[0].ImageInput == nil || !*groups[0].ImageInput {
		t.Fatalf("group with a member that sees lost images: %+v", groups)
	}
}

// A group takes every kind a member takes, not text and image alone
// (#2898, #6135), and says nothing of the kinds when no member did.
func TestGroupInputsAreItsMembers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	entries := []Entry{
		{ID: "a/m", Model: "m", Provider: Provider{ID: "a"}, Images: true, Inputs: []string{"text", "image"}, Outputs: []string{"text"}},
		{ID: "b/m", Model: "m", Provider: Provider{ID: "b"}, Images: true, Inputs: []string{"text", "image", "video"}, Outputs: []string{"text"}},
		{ID: "a/n", Model: "n", Provider: Provider{ID: "a"}},
		{ID: "b/n", Model: "n", Provider: Provider{ID: "b"}},
	}
	got := map[string]Entry{}
	for _, g := range groupEntries(entries) {
		got[g.Model] = g
	}
	if g := got["m"]; !slices.Equal(g.Inputs, []string{"text", "image", "video"}) || !slices.Equal(g.Outputs, []string{"text"}) {
		t.Errorf("group of m takes %v, gives %v", g.Inputs, g.Outputs)
	}
	if g, ok := got["n"]; !ok || g.Inputs != nil || g.Outputs != nil {
		t.Errorf("group of n, no member said of: %+v (%v)", g, ok)
	}
}

// A group whose first member is a ChatGPT account's model is still magpie's
// to list to a signed-in Codex: the backend lists the model, not the group.
func TestCodexListedKeepsGroups(t *testing.T) {
	acct := Provider{ID: "codex-me", Account: &Account{Agent: "codex"}}
	ms := codexListed([]Entry{
		{ID: GroupPrefix + "fast", Model: "gpt-5.5", Name: "fast", Provider: acct, Group: "fast"},
		{ID: "codex-me/gpt-5.5", Model: "gpt-5.5", Provider: acct},
		{ID: "or/glm-5.3", Model: "glm-5.3", Provider: Provider{ID: "or"}},
	}, func(id string) []Member {
		return []Member{{Provider: Provider{ID: "or"}, Model: "glm-5.3"}, {Provider: acct, Model: "gpt-5.5"}}
	}, false)
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	if len(ids) != 2 || ids[0] != GroupPrefix+"fast" || ids[1] != "or/glm-5.3" {
		t.Errorf("listed %v", ids)
	}
	// Codex offers /fast on the group, a ChatGPT GPT model being in it
	if !ms[0].Fast || ms[1].Fast {
		t.Errorf("fast: group %v, model %v", ms[0].Fast, ms[1].Fast)
	}
}

// A group keeps what Kimi Code's list says of its models (always thinks,
// loads tools mid-conversation, its default effort) when every member says
// it, and has none of it when one member doesn't (jiabubu on Discord: a Kimi
// model put in a group lost always_thinking and dynamically_loaded_tools in
// Kimi Code).
func TestGroupSharesKimiFacts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	e := func(p, m string, always, dynamic bool, def string) Entry {
		return Entry{ID: p + "/" + m, Model: m, Name: m, Provider: Provider{ID: p}, Reasoning: true,
			Efforts: []string{"low", "medium", "high"}, AlwaysThinks: always, DynamicTools: dynamic, DefaultEffort: def}
	}
	type facts struct {
		always, dynamic bool
		def             string
	}
	got := map[string]facts{}
	for _, g := range groupEntries([]Entry{
		e("kimi-code", "kimi-k3", true, true, "high"), e("kimi-code-cn", "kimi-k3", true, true, "high"),
		e("kimi-code", "kimi-k2.7", true, true, "high"), e("or", "kimi-k2.7", false, false, ""),
		e("kimi-code", "kimi-k2.8", true, false, "medium"), e("kimi-code-cn", "kimi-k2.8", true, true, "high"),
	}) {
		got[g.ID] = facts{g.AlwaysThinks, g.DynamicTools, g.DefaultEffort}
	}
	want := map[string]facts{
		AutoPrefix + "kimi-k3":   {true, true, "high"},
		AutoPrefix + "kimi-k2.7": {},
		AutoPrefix + "kimi-k2.8": {always: true},
	}
	if !maps.Equal(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// WorkBuddy AI lists deepseek-v4.1-flash under its paid id, -sg, so the
// two editions' one model merged by id alone stayed two (Kts6M2fwWN43967
// on X). The twin joins the group; another vendor's -sg id, and WorkBuddy
// AI's other models, are unchanged.
func TestAutoGroupsEditionTwins(t *testing.T) {
	e := func(p, m string) Entry {
		return Entry{ID: p + "/" + m, Model: m, Name: "DeepSeek-V4.1-Flash", Provider: Provider{ID: p}}
	}
	gs := autoGroups([]Entry{e("workbuddy", "deepseek-v4.1-flash"), e("workbuddy-ai", "deepseek-v4.1-flash-sg")}, nil)
	if len(gs) != 1 || gs[0].ID != "auto-deepseek-v4-1-flash" || len(gs[0].Members) != 2 {
		t.Fatalf("groups: %+v", gs)
	}
	if gs := autoGroups([]Entry{e("workbuddy", "deepseek-v4.1-flash"), e("other", "deepseek-v4.1-flash-sg")}, nil); len(gs) != 0 {
		t.Errorf("another vendor's -sg model merged: %+v", gs)
	}
	// the user's own "same as" is read first
	same := map[string]string{"workbuddy-ai/deepseek-v4.1-flash-sg": "deepseek-v4.1-flash-sg"}
	if gs := autoGroups([]Entry{e("workbuddy", "deepseek-v4.1-flash"), e("workbuddy-ai", "deepseek-v4.1-flash-sg")}, same); len(gs) != 0 {
		t.Errorf("the user's own same-as lost: %+v", gs)
	}
}
