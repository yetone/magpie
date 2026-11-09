package provider

import (
	"slices"
	"testing"
)

// ownEntries: Codex's ChatGPT account, Devin's plugin and a relay all
// serve gpt-6-astra (freefcw on X), the relay first in the order; only the
// relay and Devin serve glm-6.
func ownEntries() []Entry {
	codex := Provider{ID: "codex", Account: &Account{Agent: "codex", User: "a@example.com"}}
	devin := Provider{ID: "devin-plugin", Account: &Account{Agent: "plugin"}}
	relay := Provider{ID: "relay"}
	e := func(p Provider, m string) Entry { return Entry{ID: p.ID + "/" + m, Model: m, Provider: p} }
	return []Entry{
		e(relay, "gpt-6-astra"), e(relay, "glm-6"),
		e(devin, "gpt-6-astra"), e(devin, "glm-6"),
		e(codex, "gpt-6-astra"),
	}
}

// A bare name the asking agent's own subscription serves goes there,
// whether found groups are on (a group of it found) or off (none), and
// says the found group it went past; a group the user made by that name,
// a provider's id, a group's ref and another agent's request don't.
func TestOwnRoute(t *testing.T) {
	entries := ownEntries()
	on := autoGroups(entries, nil)
	for _, c := range []struct {
		name      string
		groups    []Group
		agent, id string
		to, group string
	}{
		{"on", on, "codex", "gpt-6-astra", "codex/gpt-6-astra", "group/auto-gpt-6-astra"},
		{"off", nil, "codex", "gpt-6-astra", "codex/gpt-6-astra", ""},
		{"1m mark", on, "codex", "gpt-6-astra[1m]", "codex/gpt-6-astra", "group/auto-gpt-6-astra"},
		{"another agent", on, "claude", "gpt-6-astra", "", ""},
		{"no agent", on, "", "gpt-6-astra", "", ""},
		{"a plugin", on, "plugin", "gpt-6-astra", "", ""},
		{"not its own", on, "codex", "glm-6", "", ""},
		{"provider named", on, "codex", "relay/gpt-6-astra", "", ""},
		{"group ref", on, "codex", "group/auto-gpt-6-astra", "", ""},
		{"auto ref", on, "codex", "auto/gpt-6-astra", "", ""},
		{"the user's group", []Group{{ID: "gpt-6-astra", Name: "Mine", Members: []string{"relay/gpt-6-astra"}}}, "codex", "gpt-6-astra", "", ""},
	} {
		to, group, ok := ownRouteIn(entries, c.groups, c.agent, c.id)
		if to != c.to || group != c.group || ok != (c.to != "") {
			t.Errorf("%s: %q %q %v, want %q %q", c.name, to, group, ok, c.to, c.group)
		}
	}
	// spelt otherwise by the vendor (sameModel), still its own
	dotted := []Entry{{ID: "codex/gpt-6.1-sol", Model: "gpt-6.1-sol", Provider: Provider{ID: "codex", Account: &Account{Agent: "codex"}}}}
	if to, _, ok := ownRouteIn(dotted, nil, "codex", "gpt-6-1-sol"); !ok || to != "codex/gpt-6.1-sol" {
		t.Errorf("by its vendor's spelling: %q %v", to, ok)
	}
}

// With found groups off, a bare name several providers serve, none of
// them the agent's own, names them all; one the agent's own serves, one
// provider serves, or a group of the user's has, names none.
func TestOffSpread(t *testing.T) {
	entries := ownEntries()
	if got := offSpreadIn(entries, nil, nil, "codex", "glm-6"); !slices.Equal(got, []string{"relay/glm-6", "devin-plugin/glm-6"}) {
		t.Errorf("glm-6: %v", got)
	}
	if got := offSpreadIn(entries, nil, nil, "codex", "gpt-6-astra"); got != nil {
		t.Errorf("its own serves it: %v", got)
	}
	one := entries[:2]
	if got := offSpreadIn(one, nil, nil, "codex", "glm-6"); got != nil {
		t.Errorf("one provider: %v", got)
	}
	mine := []Group{{ID: "glm-6", Name: "Mine", Members: []string{"relay/glm-6"}}}
	if got := offSpreadIn(entries, mine, nil, "codex", "glm-6"); got != nil {
		t.Errorf("the user's group: %v", got)
	}
}
