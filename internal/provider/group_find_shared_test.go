package provider

import (
	"fmt"
	"runtime"
	"testing"
)

// totalAlloc is the bytes allocated by f, run n times.
func totalAlloc(n int, f func()) uint64 {
	var a, b runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&a)
	for range n {
		f()
	}
	runtime.ReadMemStats(&b)
	return b.TotalAlloc - a.TotalAlloc
}

// A routing group's lookup copied every group and its rules for the one it
// wanted: 4 MB of the window's 15-second drift poll (each Pi model on a
// group asked GroupFinder), and once per request on a group. While a
// request holds the catalog it reads the groups held and copies the one
// found, and the groups its members come through, alone: what a caller
// is given is still its own to change.
func TestGroupFoundCopiesItselfAlone(t *testing.T) {
	groupHeldFixture(t)
	if err := SaveGroup(Group{ID: "outer", Name: "Outer", Members: []string{"group/g4", "p5/m2"}, Routing: Ordered,
		EffortMaps: map[string]map[string]string{"p5/m2": {"high": "low"}}}); err != nil {
		t.Fatal(err)
	}
	// a user's many groups, each with rules: what every look copied
	for i := range 100 {
		if err := SaveGroup(Group{ID: fmt.Sprintf("x%d", i), Name: fmt.Sprintf("X%d", i), Routing: Ordered,
			Members: []string{"p1/m3", "p2/m3", "p3/m3", "p4/m3"},
			Rules:   []Rule{{Use: "p2/m3", Tokens: 10, Agents: []string{"codex"}}}}); err != nil {
			t.Fatal(err)
		}
	}
	Changed()
	release := Hold()
	defer release()
	g, ms, ok := FindGroup("group/outer")
	if !ok || len(ms) < 2 || len(ms[0].Via) == 0 {
		t.Fatalf("%v %d %+v", ok, len(ms), ms)
	}
	// every part a caller may change is its own
	g.Members[0] = "changed"
	ms[0].Via[0].Members[0] = "changed"
	for _, m := range ms {
		for k := range m.EffortMap {
			m.EffortMap[k] = "changed"
		}
	}
	find := GroupFinder()
	for _, look := range []func(string) (Group, []Member, bool){FindGroup, find} {
		g2, ms2, _ := look("group/outer")
		if g2.Members[0] != "group/g4" || ms2[0].Via[0].Members[0] != "p0/m1" {
			t.Fatalf("a change to a found group reached the next look: %v %v", g2.Members, ms2[0].Via[0].Members)
		}
		for _, m := range ms2 {
			if m.EffortMap != nil && m.EffortMap["high"] != "low" {
				t.Fatalf("a change to a member's effort map reached the next look: %v", m.EffortMap)
			}
		}
	}
	for _, x := range Groups() {
		if x.ID == "g4" && x.Members[0] != "p0/m1" || x.ID == "outer" && x.Members[0] != "group/g4" {
			t.Fatalf("a change to a found group reached Groups: %s %v", x.ID, x.Members)
		}
	}
	// and a look costs less than one copy of every group
	all := sharedGroupsIn(providerEntries())
	if len(all) < 100 {
		t.Fatalf("the fixture has %d groups; the test needs the found ones too", len(all))
	}
	const n = 50
	copies := totalAlloc(n, func() { _ = cloneGroups(all) })
	looks := totalAlloc(n, func() {
		if _, _, ok := GroupFinder()("group/g4"); !ok {
			t.Fatal("no group g4")
		}
	})
	if looks >= copies {
		t.Fatalf("%d looks allocated %d bytes, as much as %d copies of every group (%d)", n, looks, n, copies)
	}
}
