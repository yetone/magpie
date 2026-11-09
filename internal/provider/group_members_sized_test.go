package provider

import (
	"fmt"
	"testing"
)

// TestGroupMembersMadeAtSize says a group's members are listed in one
// slice made at the group's count, not grown by append: a Member is over
// a kilobyte, and the growing was 9 MB of what forty polls of the
// Providers page allocated with real accounts. The members are the same
// either way, in the same order, which the other group tests hold. Forty
// members, as append grows a slice of Members to 17 and 34: at 33 it
// happened to end with room for the group's 34 all the same.
func TestGroupMembersMadeAtSize(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var models, members []string
	for i := range 40 {
		models = append(models, fmt.Sprintf("m%d", i))
		members = append(members, fmt.Sprintf("a/m%d", i))
	}
	if err := Save(Provider{ID: "a", Name: "a", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: models}); err != nil {
		t.Fatal(err)
	}
	g := Group{ID: "big", Members: append(members, "gone/x"), Routing: Ordered}
	ms := membersIn(providerEntries(), []Group{g}, g)
	if len(ms) != 40 {
		t.Fatalf("%d members, want 40", len(ms))
	}
	for i, m := range ms {
		if m.Model != models[i] || len(m.Path) != 1 || m.Path[0] != members[i] {
			t.Fatalf("member %d: %s %v, want %s", i, m.Model, m.Path, models[i])
		}
	}
	if cap(ms) != len(g.Members) {
		t.Errorf("members made with room for %d, want the group's %d: grown by append", cap(ms), len(g.Members))
	}
	// a manual group has one member, and room for one
	g.Routing, g.Pick = Manual, "a/m3"
	if ms := membersIn(providerEntries(), []Group{g}, g); len(ms) != 1 || cap(ms) != 1 || ms[0].Model != "m3" {
		t.Errorf("manual: %d members, room for %d", len(ms), cap(ms))
	}
	// none found is none, as before
	if ms := membersIn(providerEntries(), nil, Group{ID: "e", Members: []string{"gone/x"}}); ms != nil {
		t.Errorf("none found: %#v, want nil", ms)
	}
}
