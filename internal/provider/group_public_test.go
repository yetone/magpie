package provider

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

// A group shared at a link anyone opens (usemagpie.ai/g/…) names a
// provider the user added themselves "custom", in every field that names
// a member: what they called their relay is theirs. A preset keeps its
// built-in id, the model ids stay, and so does a group member.
func TestPublicGroupNamesOwnProvidersCustom(t *testing.T) {
	shareHome(t,
		Provider{ID: "myrelay", Name: "My Relay", Key: "sk-relay", Chat: "http://127.0.0.1:1/v1", Models: []string{"gpt-6", "small"}},
		Provider{ID: "ds-work", Name: "DeepSeek (work)", Preset: "deepseek", Key: "sk-ds", Chat: "https://api.deepseek.com", Models: []string{"deepseek-v4"}},
	)
	if err := SaveGroup(Group{Name: "Inner", Members: []string{"myrelay/small"}}); err != nil {
		t.Fatal(err)
	}
	g := Group{Name: "Mixed", Routing: Ordered,
		Members:    []string{"myrelay/gpt-6:high", "ds-work/deepseek-v4", "group/inner"},
		Classifier: "myrelay/small", Fast: []string{"myrelay/gpt-6:high"}, Off: []string{"ds-work/deepseek-v4"},
		Weights: map[string]int{"myrelay/gpt-6:high": 3},
		Rules:   []Rule{{Use: "myrelay/gpt-6:high", Tokens: 50000}, {Use: "ds-work/deepseek-v4", Images: true}, {Use: "ds-work/deepseek-v4", Intent: "a quick question"}},
		Former:  []string{"old-mixed"}, RoutedAt: time.Now()}
	if err := SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	s, err := PublicGroup("mixed")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(s)
	for _, leak := range []string{"myrelay", "ds-work", "My Relay", "sk-", "127.0.0.1", "old-mixed", "routedAt"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("the public group carries %q: %s", leak, raw)
		}
	}
	want := []string{"custom/gpt-6:high", "deepseek/deepseek-v4", "group/inner"}
	if !slices.Equal(s.Group.Members, want) {
		t.Errorf("members %v, want %v", s.Group.Members, want)
	}
	if s.Group.Classifier != "custom/small" || !slices.Equal(s.Group.Fast, []string{"custom/gpt-6:high"}) || !slices.Equal(s.Group.Off, []string{"deepseek/deepseek-v4"}) {
		t.Errorf("classifier %q, fast %v, off %v", s.Group.Classifier, s.Group.Fast, s.Group.Off)
	}
	if s.Group.Weights["custom/gpt-6:high"] != 3 || s.Group.Rules[0].Use != "custom/gpt-6:high" || s.Group.Rules[1].Use != "deepseek/deepseek-v4" {
		t.Errorf("weights %v, rules %+v", s.Group.Weights, s.Group.Rules)
	}
	// and the group here is as it was
	if here := sharedAt(t, "mixed"); !slices.Equal(here.Members, g.Members) {
		t.Errorf("sharing changed the group here: %v", here.Members)
	}
}

// Text shared from one magpie never carries the ids the group had there
// before a rename: here, a request asking one would mean another group.
func TestSharedGroupDropsFormerIDs(t *testing.T) {
	shareHome(t, Provider{ID: "a", Name: "A", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"x"}})
	if err := SaveGroup(Group{Name: "Mine", Members: []string{"a/x"}, Former: []string{"was-mine"}, RoutedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	text, err := ShareGroup("mine")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "was-mine") || strings.Contains(text, "routedAt") {
		t.Fatalf("shared text names the group's former id or when it was routed: %s", text)
	}
	s, _ := ReadShared(text)
	s.Group.Former = []string{"sneaked-in"}
	s.Group.ID = "other"
	res, err := ImportShared(s, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if g := sharedAt(t, res.ID); len(g.Former) != 0 {
		t.Errorf("an imported group answers to the sender's old ids %v", g.Former)
	}
}

// Before importing, the preview says each member as it would be here and
// saves nothing; the user may send a member to another provider's same
// model, or leave it out, and the import does what the preview showed.
func TestPlanImportThenUse(t *testing.T) {
	shareHome(t,
		Provider{ID: "a", Name: "A", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"x", "y"}},
		Provider{ID: "b", Name: "B", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"x"}},
	)
	s := SharedGroup{Magpie: ShareMark, Version: ShareVersion, Group: Group{ID: "shared", Name: "Shared", Routing: Ordered,
		Members: []string{"a/x", "custom/y", "zz/none"},
		Rules:   []Rule{{Use: "zz/none", Tokens: 1000}, {Use: "a/x", Images: true}}}}
	before := len(Groups())
	plan := PlanImport(s, nil, false)
	if len(Groups()) != before {
		t.Fatal("PlanImport saved a group")
	}
	if plan.Name != "Shared" || len(plan.Members) != 3 || plan.Rules != 2 || plan.Dropped != 1 || plan.Empty {
		t.Fatalf("plan %+v", plan)
	}
	m := plan.Members
	if m[0].Here != "a/x" || !slices.Contains(m[0].Same, "b/x") {
		t.Errorf("a/x is here, with b/x the same model: %+v", m[0])
	}
	if m[1].Here != "a/y" {
		t.Errorf("custom/y is a/y here, the one provider serving y: %+v", m[1])
	}
	if m[2].Here != "" {
		t.Errorf("zz/none is nowhere here: %+v", m[2])
	}

	use := map[string]string{"a/x": "b/x", "custom/y": ""}
	plan = PlanImport(s, use, false)
	if plan.Members[0].Here != "b/x" || plan.Members[1].Here != "" {
		t.Fatalf("the plan doesn't follow the user's picks: %+v", plan.Members)
	}
	res, err := ImportShared(s, use, false)
	if err != nil {
		t.Fatal(err)
	}
	g := sharedAt(t, res.ID)
	if !slices.Equal(g.Members, []string{"b/x"}) {
		t.Errorf("imported members %v, want [b/x]", g.Members)
	}
	if len(g.Rules) != 1 || g.Rules[0].Use != "b/x" {
		t.Errorf("the rule sending to a/x follows it to b/x: %+v", g.Rules)
	}
	// a pick that isn't here is no pick: the member is left out
	plan = PlanImport(s, map[string]string{"a/x": "q/nothing"}, false)
	if plan.Members[0].Here != "" {
		t.Errorf("a pick this magpie hasn't is %q, want left out", plan.Members[0].Here)
	}
	if MembersHere([]string{"a/x", "custom/y", "zz/none"}) != 2 {
		t.Errorf("MembersHere = %d, want 2", MembersHere([]string{"a/x", "custom/y", "zz/none"}))
	}
}

// Importing a newer version over the group it came from replaces that
// group: same id, nothing added beside it.
func TestImportSharedOverReplaces(t *testing.T) {
	shareHome(t, Provider{ID: "a", Name: "A", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"x", "y"}})
	s := SharedGroup{Magpie: ShareMark, Version: ShareVersion, Group: Group{Name: "Theirs", Members: []string{"a/x"}}}
	res, err := ImportShared(s, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	n := len(Groups())
	s.Group.Members = []string{"a/x", "a/y"}
	s.Group.ID = res.ID
	if plan := PlanImport(s, nil, true); plan.Replaces != res.ID {
		t.Errorf("the plan doesn't say it replaces %s: %+v", res.ID, plan)
	}
	again, err := ImportShared(s, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != res.ID || !again.Replaced || len(Groups()) != n {
		t.Fatalf("over: %+v, groups %d → %d", again, n, len(Groups()))
	}
	if g := sharedAt(t, res.ID); !slices.Equal(g.Members, []string{"a/x", "a/y"}) {
		t.Errorf("members %v", g.Members)
	}
}
