package gui

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The Providers page finds each group's members once, however many agents
// are on it, and the gateway's list takes the same: each find built every
// member, a kilobyte and more each, again.
func TestGroupsFoundOncePerPage(t *testing.T) {
	calls := map[string]int{}
	find := groupsOnce(func(id string) (provider.Group, []provider.Member, bool) {
		calls[id]++
		if id != "group/a" {
			return provider.Group{}, nil, false
		}
		return provider.Group{ID: "a"}, []provider.Member{{Model: "m"}}, true
	})
	for range 3 {
		if g, ms, ok := find("group/a"); !ok || g.ID != "a" || len(ms) != 1 || ms[0].Model != "m" {
			t.Fatalf("group/a: %v %v %v", g, ms, ok)
		}
		if _, _, ok := find("group/gone"); ok {
			t.Fatal("a group not found was found")
		}
	}
	if calls["group/a"] != 1 || calls["group/gone"] != 1 {
		t.Errorf("finds: %v, want each once", calls)
	}
}
