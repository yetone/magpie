package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A member typed on an account of its own (EastonXvii on X) is the model
// resolved as any other, at its effort and fast, and the account after
// it, kept by its id though typed by its name: the same model on two
// accounts is two members, in the order typed. A group on an account,
// and an account the provider lacks, are refused.
func TestGroupMembersOnAccounts(t *testing.T) {
	groupsHome(t)
	p, err := provider.Find("a")
	if err != nil {
		t.Fatal(err)
	}
	p.Keys = []provider.KeyAccount{{Key: "ka2", Name: "Work"}}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	work, mine := provider.KeyID("ka2"), provider.KeyID("ka")
	g, err := addGroup("Chain", []string{"models=a/m#Work,m:high#" + mine + ",b/gpt-5.5", "routing=order"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a/m#" + work, "a/m:high#" + mine, "b/gpt-5.5"}; !slices.Equal(g.Members, want) {
		t.Fatalf("members %v, want %v", g.Members, want)
	}
	if s, ok := memberLabel(g.Members[0], catalogByID()); !ok || !strings.Contains(s, "Work") {
		t.Errorf("label %q %v", s, ok)
	}
	for _, bad := range []string{"a/m#nobody", "group/chain#" + work} {
		if _, err := addGroup("Bad", []string{"models=a/m," + bad}); err == nil {
			t.Errorf("%s: added", bad)
		}
	}
}
