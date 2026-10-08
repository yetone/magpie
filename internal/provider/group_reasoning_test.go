package provider

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// A group reasons when a member does: a member that doesn't think is no
// reason to tell the agent the group can't, as a text-only member is no
// reason to say it can't see (#756's rule for images, applied to
// reasoning).
//
// Before this, one member that didn't think made the whole group
// 不支持推理, and the agent turned thinking off for it: a routing group of
// deepseek-v4-flash-0731-max (a Trae plugin model magpie read as one that
// doesn't reason) with a DeepSeek that does lost reasoning for both.
//
// What the group offers as levels is not changed here: a member known to
// take none still leaves the group none (#597), and the group is then
// Levelless — magpie's shape for a model that thinks with no level to
// pick from — rather than 不支持推理.
func TestGroupReasoningFromAMemberThatThinks(t *testing.T) {
	groupReasoningHome(t)
	for name, in := range map[string][]Entry{
		// the one that doesn't think is the group's own model (its first
		// member's), so the group's own entry says nothing of reasoning
		"quiet first": {quietMember(), thinksMember()},
		"quiet last":  {thinksMember(), quietMember()},
	} {
		g := groupEntries(in)
		if len(g) != 1 {
			t.Fatalf("%s: groups %+v", name, g)
		}
		if !g[0].Reasoning {
			t.Errorf("%s: a member thinks, but the group says it doesn't", name)
		}
	}
}

// A group of members that don't think reasons for none of them.
func TestGroupReasoningFromNoMember(t *testing.T) {
	groupReasoningHome(t)
	g := groupEntries([]Entry{quiet("q"), quiet("r")})
	if len(g) != 1 {
		t.Fatalf("groups: %+v", g)
	}
	if g[0].Reasoning || len(g[0].Efforts) != 0 {
		t.Fatalf("no member thinks: reasoning %v, levels %v", g[0].Reasoning, g[0].Efforts)
	}
}

// A group that thinks offers the levels its members have in common, as
// before: a member that thinks with none of its own doesn't take the
// others' away (the #597 rule for a model whose levels nothing knows), and
// two that think share theirs.
func TestGroupReasoningLevels(t *testing.T) {
	groupReasoningHome(t)
	for name, c := range map[string]struct {
		in   []Entry
		want []string
	}{
		"one thinks":       {[]Entry{thinksMember(), quietMember()}, nil},
		"quiet first":      {[]Entry{quietMember(), thinksMember()}, nil},
		"two think":        {[]Entry{thinksMember(), thinks("b", []string{"low", "high", "medium"})}, []string{"low", "high"}},
		"thinks no levels": {[]Entry{thinks("a", nil), thinks("b", []string{"low", "high"})}, []string{"low", "high"}},
	} {
		g := groupEntries(c.in)
		if len(g) != 1 {
			t.Fatalf("%s: groups %+v", name, g)
		}
		if !slices.Equal(g[0].Efforts, c.want) {
			t.Errorf("%s: levels %v, want %v", name, g[0].Efforts, c.want)
		}
		if !g[0].Reasoning {
			t.Errorf("%s: reasoning off", name)
		}
	}
}

// A group that names its own levels offers those, member that doesn't
// think or not (Group.Levels, #295); one that doesn't think itself is
// still told to reason, its levels named.
func TestGroupReasoningNamedLevels(t *testing.T) {
	groupReasoningHome(t)
	if err := SaveGroup(Group{Name: "named", Members: []string{"a/m", "a/n"}, Levels: []string{"low", "high"}}); err != nil {
		t.Fatal(err)
	}
	for _, g := range groupEntries(append([]Entry{quietMember()}, Catalog()...)) {
		if g.ID != GroupPrefix+"named" {
			continue
		}
		if !g.Reasoning || !slices.Equal(g.Efforts, []string{"low", "high"}) {
			t.Fatalf("named levels: reasoning %v, levels %v", g.Reasoning, g.Efforts)
		}
		return
	}
	t.Fatal("no group named")
}

// A member that doesn't think doesn't take the other members' images,
// window or reply limit either: those aggregate as they did (#756, #700).
func TestGroupReasoningQuietMemberKeepsTheRest(t *testing.T) {
	groupReasoningHome(t)
	yes := true
	quiet := quietMember()
	quiet.Context, quiet.Output = 200000, 8000
	other := thinksMember()
	other.Images, other.ImageInput, other.Context, other.Output = true, &yes, 1000000, 64000
	g := groupEntries([]Entry{quiet, other})
	if len(g) != 1 {
		t.Fatalf("groups: %+v", g)
	}
	e := g[0]
	if !e.Images || e.ImageInput == nil || !*e.ImageInput || e.Context != 1000000 || e.Output != 64000 {
		t.Fatalf("a quiet member took the rest: %+v", e)
	}
	if !e.Reasoning {
		t.Fatalf("reasoning off: %+v", e)
	}
}

// The group a Trae plugin model was reported in: its provider is signed in
// (so its levels are known to be none, not unknown — it is the plugin's own
// list that says nothing of reasoning), it has no levels, and the DeepSeek
// beside it has some. The group is told to reason, whatever it offers as
// levels.
func TestGroupReasoningOfAPluginModelThatDoesNotThink(t *testing.T) {
	groupReasoningHome(t)
	trae := Provider{ID: "trae", Name: "Trae", Key: "k", Chat: "http://127.0.0.1:1/v1",
		Models:  []string{"deepseek-v4-flash-0731-max"},
		Account: &Account{Agent: "plugin", models: func() []catalog.Model { return nil }}}
	if err := Save(trae); err != nil {
		t.Fatal(err)
	}
	if err := SaveGroup(Group{Name: "mix", Members: []string{"trae/deepseek-v4-flash-0731-max", "a/m"}}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range Catalog() {
		if e.ID != GroupPrefix+"mix" {
			continue
		}
		found = true
		if !e.Reasoning {
			t.Errorf("the group is 不支持推理, though a member thinks")
		}
	}
	if !found {
		t.Fatal("no group mix")
	}
}

// levels are the levels of the member that thinks in these tests.
var levels = []string{"low", "high", "max"}

// thinksMember is a member that thinks, with levels.
func thinksMember() Entry { return thinks("a", levels) }

// thinks is a model that does: its own levels as given. Same model id as
// quietMember's, so the two are members of one group.
func thinks(p string, efforts []string) Entry {
	return Entry{ID: p + "/m", Model: "m", Name: "m", Provider: Provider{ID: p}, Efforts: efforts, Reasoning: true}
}

// quietMember is a member that doesn't think, and has no levels of its own:
// the shape a plugin's model had before its plugin listed capabilities. Its
// provider's account gives a model list (so its levels are known to be none,
// not unknown: the gateway asks it the effort as it is, no more than high).
func quietMember() Entry {
	e := quiet("q")
	e.Provider.Account = &Account{Agent: "plugin", models: func() []catalog.Model { return nil }}
	return e
}

// quiet is a model that doesn't think: its provider gives no levels for it,
// and nothing says it reasons.
func quiet(p string) Entry {
	return Entry{ID: p + "/m", Model: "m", Name: "m", Provider: Provider{ID: p}}
}

// groupReasoningHome is a machine of its own with a provider whose model
// has levels the user set.
func groupReasoningHome(t *testing.T) {
	t.Helper()
	effortHome(t)
	if err := SetModelEfforts("a/m", levels); err != nil {
		t.Fatal(err)
	}
}
