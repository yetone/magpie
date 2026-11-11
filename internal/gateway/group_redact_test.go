package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A routing group says whether a request through it is masked
// (Group.Redact, 1dwsdqwd2 on X): off sends it as written to every member
// whatever its provider says, on masks it to a member set to go unmasked,
// and unset leaves each member's provider to say. Every route that reaches
// the group asks: the group asked for, a provider's fallback that names it,
// a group in a group, and a group built around the model that the request
// goes on to (provider.GroupsAfter).
func groupRedactFixture(t *testing.T, near, far string) {
	t.Helper()
	fresh(t)
	for _, p := range []provider.Provider{
		{ID: "near", Name: "Near", Key: "k1", Models: []string{"m"}, Chat: "http://127.0.0.1:1/v1", Unredacted: true},
		{ID: "near2", Name: "Near2", Key: "k2", Models: []string{"m"}, Chat: "http://127.0.0.1:2/v1", Unredacted: true},
		{ID: "far", Name: "Far", Key: "k3", Models: []string{"m"}, Chat: "https://api.example.com/v1"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "mixed", Name: "Mixed", Members: []string{"near/m", "far/m"}, Redact: far}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "local", Name: "Local", Members: []string{"near/m", "near2/m"}, Redact: near}); err != nil {
		t.Fatal(err)
	}
}

func TestGroupRedactOffSendsAsWritten(t *testing.T) {
	groupRedactFixture(t, "", provider.RedactOff)
	_, ms, ok := provider.FindGroup("group/mixed")
	if !ok || !unmaskedMembers(ms) {
		t.Fatalf("a group set to go as written masked a member of a remote provider (ok %v)", ok)
	}
}

func TestGroupRedactUnsetFollowsProviders(t *testing.T) {
	groupRedactFixture(t, "", "")
	_, mixed, _ := provider.FindGroup("group/mixed")
	_, local, _ := provider.FindGroup("group/local")
	if unmaskedMembers(mixed) || !unmaskedMembers(local) {
		t.Fatalf("unset: mixed unmasked %v, local unmasked %v", unmaskedMembers(mixed), unmaskedMembers(local))
	}
}

func TestGroupRedactOnMasksUnmaskedProviders(t *testing.T) {
	groupRedactFixture(t, provider.RedactOn, "")
	_, local, _ := provider.FindGroup("group/local")
	if unmaskedMembers(local) {
		t.Fatal("a group set to be masked sent as written to providers set to go unmasked")
	}
	p, model, ok := provider.Resolve("group/local")
	if !ok {
		t.Fatal("group/local doesn't resolve")
	}
	if _, ms, isGroup := provider.FindGroup("group/local"); unredactedRoute(p, model, isGroup, ms) || MaskRoute("group/local") {
		t.Fatal("the route of a masked group is unredacted")
	}
}

// A provider's fallback that names a group is that group's members as the
// group has them.
func TestGroupRedactReachesAFallbackGroup(t *testing.T) {
	for _, tc := range []struct {
		group, want string
		unmasked    bool
	}{{provider.RedactOff, "off", true}, {"", "unset", false}, {provider.RedactOn, "on", false}} {
		groupRedactFixture(t, "", tc.group)
		near, err := provider.Find("near")
		if err != nil {
			t.Fatal(err)
		}
		near.Fallback = []string{"group/mixed"}
		if err := provider.Save(*near); err != nil {
			t.Fatal(err)
		}
		near, _ = provider.Find("near")
		if got := unredactedRoute(*near, "m", false, nil); got != tc.unmasked {
			t.Errorf("a fallback group %s: unredacted %v, want %v", tc.want, got, tc.unmasked)
		}
	}
}

// A provider set to go unmasked keeps its requests masked when a group
// built around its model may send them on to a model that is masked (the
// request that can't be taken goes on, provider.GroupsAfter), and a group
// that sends as written lets it keep them.
func TestGroupRedactReachesAGroupAfterTheModel(t *testing.T) {
	for _, tc := range []struct {
		group    string
		unmasked bool
	}{{provider.RedactOff, true}, {"", false}, {provider.RedactOn, false}} {
		groupRedactFixture(t, "", tc.group)
		near, err := provider.Find("near")
		if err != nil {
			t.Fatal(err)
		}
		if got := unredactedRoute(*near, "m", false, nil); got != tc.unmasked {
			t.Errorf("a group after near/m set %q: unredacted %v, want %v", tc.group, got, tc.unmasked)
		}
	}
}

// Of a group in a group, any group set to be masked masks its members, and
// otherwise any set to send as written does.
func TestGroupRedactInANestedGroup(t *testing.T) {
	for _, tc := range []struct {
		outer, inner string
		unmasked     bool
	}{{provider.RedactOff, "", true}, {"", provider.RedactOff, true}, {provider.RedactOff, provider.RedactOn, false}, {provider.RedactOn, provider.RedactOff, false}, {"", "", false}} {
		groupRedactFixture(t, "", "")
		if err := provider.SaveGroup(provider.Group{ID: "mixed", Name: "Mixed", Members: []string{"near/m", "far/m"}, Redact: tc.inner}); err != nil {
			t.Fatal(err)
		}
		if tc.inner == "" {
			if err := provider.SaveGroup(provider.Group{ID: "mixed", Name: "Mixed", Members: []string{"near/m", "far/m"}, Redact: provider.RedactFollow}); err != nil {
				t.Fatal(err)
			}
		}
		if err := provider.SaveGroup(provider.Group{ID: "outer", Name: "Outer", Members: []string{"group/mixed"}, Redact: tc.outer}); err != nil {
			t.Fatal(err)
		}
		_, ms, ok := provider.FindGroup("group/outer")
		if !ok || len(ms) == 0 {
			t.Fatalf("outer %q inner %q: no members", tc.outer, tc.inner)
		}
		if got := unmaskedMembers(ms); got != tc.unmasked {
			t.Errorf("outer %q inner %q: unmasked %v, want %v", tc.outer, tc.inner, got, tc.unmasked)
		}
	}
}

// A group saved by a caller that doesn't know of the setting keeps it;
// "follow" clears it.
func TestGroupRedactSurvivesASaveThatDoesNotKnowIt(t *testing.T) {
	groupRedactFixture(t, "", provider.RedactOff)
	if err := provider.SaveGroup(provider.Group{ID: "mixed", Name: "Mixed", Members: []string{"near/m", "far/m"}}); err != nil {
		t.Fatal(err)
	}
	g, _, _ := provider.FindGroup("group/mixed")
	if g.Redact != provider.RedactOff {
		t.Fatalf("a save that didn't name it left %q", g.Redact)
	}
	if err := provider.SaveGroup(provider.Group{ID: "mixed", Name: "Mixed", Members: []string{"near/m", "far/m"}, Redact: provider.RedactFollow}); err != nil {
		t.Fatal(err)
	}
	if g, _, _ = provider.FindGroup("group/mixed"); g.Redact != "" {
		t.Fatalf("follow left %q", g.Redact)
	}
}
