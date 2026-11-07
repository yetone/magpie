package provider

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

// A group keeps each member's effort map (LuckymanJC on Discord) under
// the member as Members spells it, its levels lowercase: a level mapped
// to itself left out, a member fixed at an effort, a group in the group
// and one not in the group dropped, and a word that isn't a level
// refused. The map follows its member when it is renamed, its provider
// is, and when the group is shared to another magpie; the member it
// resolves to carries it.
func TestSaveGroupEffortMaps(t *testing.T) {
	fastHome(t)
	maps1 := func(id string) map[string]map[string]string {
		t.Helper()
		g, _, ok := FindGroup(GroupPrefix + id)
		if !ok {
			t.Fatalf("no group %s", id)
		}
		return g.EffortMaps
	}
	if err := SaveGroup(Group{ID: "in", Name: "In", Members: []string{"rl/claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
	g := Group{ID: "relay", Name: "Relay",
		Members: []string{"oa/gpt-6.1-sol", "rl/gpt-6.1-sol:high", "an/claude-opus-5-5", "group/in"},
		EffortMaps: map[string]map[string]string{
			"oa/gpt-6.1-sol:fast": {"HIGH": "xhigh", "low": "low"},
			"rl/gpt-6.1-sol:high": {"high": "max"},
			"an/claude-opus-5-5":  {"medium": "medium"},
			"group/in":            {"high": "max"},
			"an/claude-sonnet-5":  {"high": "max"},
		}}
	if err := SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]string{"oa/gpt-6.1-sol": {"high": "xhigh"}}
	if got := maps1("relay"); !maps.EqualFunc(got, want, maps.Equal) {
		t.Fatalf("maps %v, want %v", got, want)
	}
	for _, bad := range []map[string]string{{"high": "huge"}, {"turbo": "high"}} {
		b, _, _ := FindGroup(GroupPrefix + "relay")
		b.SetMemberEffortMap("oa/gpt-6.1-sol", bad)
		if err := SaveGroup(b); err == nil || !strings.Contains(err.Error(), "reasoning level") {
			t.Fatalf("%v: %v", bad, err)
		}
	}
	_, ms, _ := FindGroup(GroupPrefix + "relay")
	i := slices.IndexFunc(ms, func(m Member) bool { return m.ID == "oa/gpt-6.1-sol" })
	if i < 0 || ms[i].EffortMap["high"] != "xhigh" {
		t.Fatalf("members %+v", ms)
	}
	for _, m := range ms {
		if m.ID != "oa/gpt-6.1-sol" && m.EffortMap != nil {
			t.Fatalf("%s carries %v", m.ID, m.EffortMap)
		}
	}

	// renamed (the TUI changing a member's effort and back), its
	// provider renamed
	saved, _, _ := FindGroup(GroupPrefix + "relay")
	saved.RenameMember("rl/gpt-6.1-sol:high", "rl/gpt-6.1-sol")
	saved.SetMemberEffortMap("rl/gpt-6.1-sol", map[string]string{"medium": "high"})
	saved.SetMemberEffortMap("an/claude-opus-5-5", map[string]string{"high": "max"})
	if err := SaveGroup(saved); err != nil {
		t.Fatal(err)
	}
	if err := Rename("rl", "relay"); err != nil {
		t.Fatal(err)
	}
	want = map[string]map[string]string{"oa/gpt-6.1-sol": {"high": "xhigh"}, "relay/gpt-6.1-sol": {"medium": "high"}, "an/claude-opus-5-5": {"high": "max"}}
	if got := maps1("relay"); !maps.EqualFunc(got, want, maps.Equal) {
		t.Fatalf("after the renames %v, want %v", got, want)
	}

	// its effort changed and back: the map goes with it
	moved, _, _ := FindGroup(GroupPrefix + "relay")
	moved.RenameMember("oa/gpt-6.1-sol", "oa/gpt-6.1-sol:low")
	if moved.MemberEffortMap("oa/gpt-6.1-sol:low")["high"] != "xhigh" || moved.MemberEffortMap("oa/gpt-6.1-sol") != nil {
		t.Fatalf("after RenameMember %v", moved.EffortMaps)
	}

	// shared to another magpie, which has OpenAI's and Anthropic's
	// models only on the relay: Anthropic's member's map goes to the
	// relay's model; the relay's own map is kept where OpenAI's member
	// is one with it
	text, err := ShareGroup("relay")
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteGroup("relay"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"oa", "an"} {
		if err := Delete(id); err != nil {
			t.Fatal(err)
		}
	}
	want = map[string]map[string]string{"relay/gpt-6.1-sol": {"medium": "high"}, "relay/claude-opus-5-5": {"high": "max"}}
	if _, err := ImportGroup(text); err != nil {
		t.Fatal(err)
	}
	var back Group
	for _, x := range Groups() {
		if x.Name == "Relay" {
			back = x
		}
	}
	if !maps.EqualFunc(back.EffortMaps, want, maps.Equal) {
		t.Fatalf("imported %v %v", back.Members, back.EffortMaps)
	}

	// an empty map sends every level as asked
	back.SetMemberEffortMap("relay/claude-opus-5-5", nil)
	back.SetMemberEffortMap("relay/gpt-6.1-sol", map[string]string{"high": "high"})
	if back.EffortMaps != nil {
		t.Fatalf("emptied %v", back.EffortMaps)
	}
}

// The levels a group lists take, for a member with a map, the levels the
// map is asked for besides its model's own.
func TestGroupLevelsWithEffortMap(t *testing.T) {
	effortHome(t)
	if err := SetModelEfforts("a/m", []string{"low", "medium", "high"}); err != nil {
		t.Fatal(err)
	}
	if err := SetModelEfforts("a/n", []string{"medium", "high", "xhigh", "max"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveGroup(Group{Name: "Mapped", Members: []string{"a/m", "a/n"}, EffortMaps: map[string]map[string]string{"a/m": {"xhigh": "high", "max": "high"}}}); err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(Catalog(), func(e Entry) bool { return e.ID == GroupPrefix+"mapped" })
	if i < 0 {
		t.Fatal("not listed")
	}
	if got := Catalog()[i].Efforts; !slices.Equal(got, []string{"medium", "high", "xhigh", "max"}) {
		t.Fatalf("levels %v", got)
	}
}
