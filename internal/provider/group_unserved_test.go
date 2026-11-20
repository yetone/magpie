package provider

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// unservedHome is a sandbox with DeepSeek-Agents (an API key) and xAI,
// and the reporter's three groups' like: X-Grok-Agent and
// DeepSeek-API-Agents, its members as given (@CHENGJIAHEGOXIS on X).
func unservedHome(t *testing.T, members ...string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, p := range []Provider{
		{ID: "deepseek-agents", Name: "DeepSeek-Agents", Key: "k", Chat: "http://ds.invalid/v1", Models: []string{"deepseek-v4.1-flash", "deepseek-v4-pro"}},
		{ID: "xai", Name: "xAI", Key: "k", Chat: "http://x.invalid/v1", Models: []string{"grok-5"}},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range []Group{
		{ID: "x-grok-agent", Name: "X-Grok-Agent", Members: []string{"xai/grok-5"}},
		{ID: "deepseek-api-agents", Name: "DeepSeek-API-Agents", Members: members},
	} {
		if err := SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
}

func shownGroups(agent string) []string {
	shown, _ := CatalogFor(agent)
	var out []string
	for _, e := range shown {
		if e.Group != "" {
			out = append(out, e.Group)
		}
	}
	return out
}

func whyOf(agent, gid string) (UnservedGroup, bool) {
	for _, u := range UnservedGroups(agent) {
		if u.Ref == GroupPrefix+gid {
			return u, true
		}
	}
	return UnservedGroup{}, false
}

// A member saved in another case than its provider lists the model by is
// the listed model: the group was taken for one of models the user had
// unticked, and left every agent's lists without a word.
func TestGroupMemberInAnotherCaseIsServed(t *testing.T) {
	unservedHome(t, "deepseek-agents/DeepSeek-V4.1-Flash", "deepseek-agents/DeepSeek-V4-Pro")
	for _, agent := range []string{"codex", "claude", "opencode"} {
		if got := shownGroups(agent); !slices.Contains(got, "deepseek-api-agents") {
			t.Errorf("%s: groups shown %v, want deepseek-api-agents", agent, got)
		}
	}
	p, m, ok := ResolveRoute("deepseek-agents/DeepSeek-V4.1-Flash")
	if !ok || p.ID != "deepseek-agents" || m != "deepseek-v4.1-flash" {
		t.Errorf("ResolveRoute = %s %q %v, want deepseek-agents deepseek-v4.1-flash", p.ID, m, ok)
	}
	if u, ok := whyOf("codex", "deepseek-api-agents"); !ok || u.Why != "" {
		t.Errorf("UnservedGroups: %+v %v, want it offered", u, ok)
	}
}

// Every group of the user's an agent's lists leave out says why, and
// whether picking it works (Pickable): none is gone without a word.
func TestUnservedGroupsSayWhy(t *testing.T) {
	members := []string{"deepseek-agents/deepseek-v4.1-flash", "deepseek-agents/deepseek-v4-pro"}
	cases := []struct {
		name string
		set  func(t *testing.T)
		why  string
	}{
		{"switched off", func(t *testing.T) {
			if err := SwitchGroup("deepseek-api-agents", false); err != nil {
				t.Fatal(err)
			}
		}, WhyGroupOff},
		{"unticked", func(t *testing.T) {
			p, err := Find("deepseek-agents")
			if err != nil {
				t.Fatal(err)
			}
			p.Models = []string{"deepseek-chat"}
			if err := Save(*p); err != nil {
				t.Fatal(err)
			}
		}, WhyGroupUnticked},
		{"turned off", func(t *testing.T) {
			if err := DisableModels("deepseek-agents/*"); err != nil {
				t.Fatal(err)
			}
		}, WhyGroupTurnedOff},
		{"only picked", func(t *testing.T) {
			if err := SetOnlyPicked("codex", true); err != nil {
				t.Fatal(err)
			}
			if err := SetPickedModels("codex", []string{"group/x-grok-agent"}); err != nil {
				t.Fatal(err)
			}
		}, WhyGroupNotPicked},
		{"taken out", func(t *testing.T) {
			if err := SetHiddenModels("codex", []string{"group/deepseek-api-agents"}); err != nil {
				t.Fatal(err)
			}
		}, WhyGroupTakenOut},
		{"narrowed", func(t *testing.T) {
			s := settings.Load()
			s.Visible = map[string][]string{"codex": {"xai", "x-grok-agent"}}
			if err := settings.Save(s); err != nil {
				t.Fatal(err)
			}
		}, WhyGroupNarrowed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			unservedHome(t, members...)
			c.set(t)
			if got := shownGroups("codex"); slices.Contains(got, "deepseek-api-agents") {
				t.Fatalf("codex is offered the group: %v", got)
			}
			u, ok := whyOf("codex", "deepseek-api-agents")
			if !ok || u.Why != c.why || u.Name != "DeepSeek-API-Agents" {
				t.Fatalf("UnservedGroups: %+v %v, want why %q", u, ok, c.why)
			}
			pickable := c.why == WhyGroupNotPicked || c.why == WhyGroupTakenOut || c.why == WhyGroupNarrowed
			if u.Pickable() != pickable {
				t.Errorf("Pickable = %v, want %v", u.Pickable(), pickable)
			}
			if g, ok := whyOf("codex", "x-grok-agent"); !ok || g.Why != "" {
				t.Errorf("X-Grok-Agent: %+v %v, want offered", g, ok)
			}
			if !pickable {
				return
			}
			// picked in the agent's picker, it is in its lists again, and
			// nothing else in them changes
			if err := ShowFor("codex", GroupPrefix+"deepseek-api-agents"); err != nil {
				t.Fatal(err)
			}
			got := shownGroups("codex")
			if !slices.Contains(got, "deepseek-api-agents") || !slices.Contains(got, "x-grok-agent") {
				t.Errorf("after ShowFor codex is offered %v", got)
			}
			if _, ok := whyOf("claude", "deepseek-api-agents"); !ok {
				t.Error("claude lost the group")
			}
		})
	}
}
