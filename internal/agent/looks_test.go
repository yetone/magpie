package agent

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/agentplug"
)

// The pages that only name and draw rows by agent poll Looks, which builds
// every agent once for a while, not on each ask; a plugin's agent coming
// or going, and the Agents page's refresh, build it again at once.
func TestLooksBuiltOnceAWhile(t *testing.T) {
	pluggedHome(t)
	old := looksFresh
	looksFresh = looksFreshInApp
	t.Cleanup(func() { looksFresh = old; forgetLooks() })
	forgetLooks()

	has := func(id string) bool {
		return slices.ContainsFunc(Looks(), func(l Look) bool { return l.ID == id })
	}
	before := looksBuilt()
	first := Looks()
	for range 5 {
		Looks()
	}
	if n := looksBuilt() - before; n != 1 {
		t.Fatalf("six asks built the agents %d times; want once", n)
	}
	cs := Clients()
	if len(first) != len(cs) {
		t.Fatalf("%d looks, %d clients", len(first), len(cs))
	}
	for i, a := range cs {
		if l := first[i]; l != (Look{a.ID, a.Name, a.SharedName(), a.Icon}) {
			t.Fatalf("look %d = %+v, client %s %q %q %q", i, l, a.ID, a.Name, a.SharedName(), a.Icon)
		}
	}
	if has("aider") {
		t.Fatal("aider looked before its plugin is on")
	}

	pluggedHome(t, aiderSrc)
	if !has("aider") {
		t.Fatal("a plugin's agent switched on isn't looked until the memo runs out")
	}
	pluggedHome(t)
	agentplug.Reload()
	if has("aider") {
		t.Fatal("a plugin's agent switched off is still looked")
	}

	before = looksBuilt()
	Rescan()
	Looks()
	if looksBuilt() == before {
		t.Fatal("the Agents page's refresh didn't build the looks again")
	}
}
