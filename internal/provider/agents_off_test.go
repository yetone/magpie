package provider

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// Models turned off for agents on the Gateway page (辰林鸭~ on Discord):
// gone from every agent's lists at once, each agent's own picks kept
// beside them, a routing group made of them still listed and still served,
// and a request naming one still served.
func TestAgentsOffLeaveEveryAgentsList(t *testing.T) {
	isolate(t)
	unservedHome(t, "deepseek-agents/deepseek-v4.1-flash", "deepseek-agents/deepseek-v4-pro")
	if err := SetHiddenModels("codex", []string{"xai/grok-5"}); err != nil {
		t.Fatal(err)
	}
	members := []string{"deepseek-agents/deepseek-v4-pro", "deepseek-agents/deepseek-v4.1-flash"}
	// a group's id and an empty one are passed by: agents keep the group
	if err := SetAgentsOff(append([]string{" deepseek-agents/deepseek-v4-pro ", "", GroupPrefix + "deepseek-api-agents"}, members...), true); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load().HiddenModels[EveryAgent]; !slices.Equal(got, members) {
		t.Fatalf("saved %v", got)
	}
	for _, agent := range []string{"claude", "codex", "opencode", "dsh", ""} {
		shown, _ := CatalogFor(agent)
		for _, m := range members {
			if slices.Contains(ids(shown), m) {
				t.Errorf("%q still lists %s: %v", agent, m, ids(shown))
			}
		}
		if !slices.Contains(shownGroups(agent), "deepseek-api-agents") {
			t.Errorf("%q lost the group made of them: %v", agent, ids(shown))
		}
		if u, ok := whyOf(agent, "deepseek-api-agents"); !ok || u.Why != "" {
			t.Errorf("%q has the group unserved: %+v %v", agent, u, ok)
		}
	}
	// codex's own pick stays its own: the grok it hid is still hidden, and
	// the agent's own list of picks isn't where the set went
	if got := settings.Load().HiddenModels["codex"]; !slices.Equal(got, []string{"xai/grok-5"}) {
		t.Fatalf("codex's own picks %v", got)
	}
	if shown, _ := CatalogFor("claude"); !slices.Contains(ids(shown), "xai/grok-5") {
		t.Fatalf("another agent lost codex's pick: %v", ids(shown))
	}
	// served as before: the catalog a request is found in, and the group's members
	all := ids(Catalog())
	for _, m := range members {
		if !slices.Contains(all, m) {
			t.Errorf("the catalog lost %s", m)
		}
		if !slices.Contains(ids(Served()), m) {
			t.Errorf("not served: %s", m)
		}
	}
	for _, m := range members {
		if _, _, ok := ResolveRoute(m); !ok {
			t.Errorf("a group can't route to %s", m)
		}
		if _, _, ok := Resolve(m); !ok {
			t.Errorf("a request can't name %s", m)
		}
	}
	if p, _, ok := Resolve(GroupPrefix + "deepseek-api-agents"); !ok || p.ID != "deepseek-agents" {
		t.Fatalf("the group resolves to %q %v", p.ID, ok)
	}
	// back on, one at a time; none left drops the key
	if err := SetAgentsOff(members[:1], false); err != nil {
		t.Fatal(err)
	}
	if shown, _ := CatalogFor("claude"); !slices.Contains(ids(shown), members[0]) || slices.Contains(ids(shown), members[1]) {
		t.Fatalf("after one back on: %v", ids(shown))
	}
	if err := SetAgentsOff(members, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := settings.Load().HiddenModels[EveryAgent]; ok {
		t.Fatal("an empty set is kept")
	}
}
